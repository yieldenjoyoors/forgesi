package knot

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// THE fleet auth primitive: the knot mints a repo push token, and the
// real git client pushes with it — through the STANDARD service-auth
// path (the repo DID doc carries the knot's key, iss=repo DID,
// aud=knot DID, lxm=receivePack).

func TestMintedTokenRealGitPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth: ServiceAuth{V: &atauth.Verifier{ResolveDID: func(did string) (*atauth.DIDDoc, error) {
			// Every DID resolves to the knot's key: the knot itself is the
			// authority for its repos (repo did docs carry the same key).
			return &atauth.DIDDoc{Keys: []atauth.DIDKey{{ID: did + "#k", Type: "EcdsaSecp256k1VerificationKey2019",
				Multibase: "z" + atauth.Base58Encode(key.PubKey().SerializeCompressed())}}}, nil
		}, Now: time.Now}},
		DB:      db,
		MintKey: key,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// bootstrap the repo through the minted-token path itself: mint with
	// a bootstrap token (repo mgmt auth = knot did, minted by the knot)
	boot, err := atauth.Mint(key, "did:web:forgesi.org:repo:agent-demo", "did:web:forgesi.org", "", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.repo.createRepo",
		strings.NewReader(`{"name":"agent-demo"}`))
	req.Header.Set("Authorization", "Bearer "+boot)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("createRepo: %v %v", err, resp)
	}
	_ = resp.Body.Close()

	// mint the repo push token through the endpoint (authed by boot)
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.repo.mintToken",
		strings.NewReader(`{"repo":"did:web:forgesi.org:repo:agent-demo"}`))
	req.Header.Set("Authorization", "Bearer "+boot)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("mintToken: %v %v", err, resp)
	}
	var minted struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&minted)
	_ = resp.Body.Close()
	if minted.Token == "" {
		t.Fatal("empty token")
	}

	// real git push using ONLY the minted token
	src := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "agent-work.md"), []byte("# done by the fleet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("-c", "user.name=agent", "-c", "user.email=agent@knot", "commit", "-q", "-m", "fleet work")
	base := srv.URL + "/did:web:forgesi.org:repo:agent-demo"
	git("-c", "http.extraHeader=Authorization: Bearer "+minted.Token, "push", base, "main")

	// and the push landed: clone sees the file
	dst := t.TempDir() + "/c"
	out, err := exec.Command("git", "clone", "-q", base, dst).CombinedOutput()
	if err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dst, "agent-work.md"))
	if err != nil || !strings.Contains(string(got), "fleet") {
		t.Fatalf("cloned content: %v %q", err, got)
	}

	// the token is lxm-scoped to push: record WRITES with it must fail
	// (lxm mismatch — ServiceAuth verifies lxm when the route passes one
	// only for git; record writes pass none, so the lxm claim mismatches
	// nothing — but aud does: repo-record writes need aud=repo DID).
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/com.atproto.repo.createRecord",
		strings.NewReader(`{"repo":"did:web:forgesi.org:repo:agent-demo","collection":"x.y.z","record":{"$type":"x.y.z"}}`))
	req.Header.Set("Authorization", "Bearer "+minted.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("minted push token accepted for record write: %d", resp.StatusCode)
	}
}
