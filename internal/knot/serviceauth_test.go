package knot

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	becdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// The full production auth path over HTTP: a PDS-minted ES256K service-auth
// JWT with aud=repo DID writes a record; a token for the WRONG audience is
// refused; no token is refused.

func TestServiceAuthRecordWriteOverHTTP(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	multibase := "z" + atauth.Base58Encode(priv.PubKey().SerializeCompressed())
	issuer := "did:plc:agent-1"

	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth: DevAuthorizer{DID: "did:web:bootstrapper"}, // create the repo with dev auth
		DB:   db,
	})
	// create repo (aud = knot did under dev auth — fine)
	req := httptest.NewRequest(http.MethodPost, "/xrpc/org.tangled.repo.createRepo", strings.NewReader(`{"name":"svc"}`))
	req.Header.Set("Authorization", "Bearer dev")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}

	// now switch the SAME knot to service auth
	h = New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth: ServiceAuth{V: &atauth.Verifier{
			ResolveDID: func(did string) (*atauth.DIDDoc, error) {
				return &atauth.DIDDoc{Keys: []atauth.DIDKey{{ID: did + "#k", Type: "EcdsaSecp256k1VerificationKey2019", Multibase: multibase}}}, nil
			},
			Now: time.Now,
		}},
		DB: db,
	})

	mint := func(aud string) string {
		header := `{"typ":"JWT","alg":"ES256K"}`
		claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"exp":%d,"lxm":"com.atproto.repo.createRecord"}`, issuer, aud, time.Now().Add(time.Minute).Unix())
		signing := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(claims))
		digest := sha256.Sum256([]byte(signing))
		sig := becdsa.Sign(priv, digest[:])
		var raw [64]byte
		r, s2 := sig.R(), sig.S()
		rb, sb := r.Bytes(), s2.Bytes()
		copy(raw[:32], rb[:])
		copy(raw[32:], sb[:])
		return signing + "." + base64.RawURLEncoding.EncodeToString(raw[:])
	}
	repo := "did:web:forgesi.org:repo:svc"
	body := `{"repo":"` + repo + `","collection":"org.tangled.track.ticket","record":{"$type":"org.tangled.track.ticket","title":{"$type":"org.tangled.markup.markdown#inline","text":"service-authed"},"state":"open","createdAt":"2026-10-31T12:00:00.000Z"}}`

	// correct audience: accepted, editor = the issuer DID
	req = httptest.NewRequest(http.MethodPost, "/xrpc/com.atproto.repo.createRecord", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mint(repo))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("service-auth write: %d %s", w.Code, w.Body.String())
	}
	var res map[string]any
	_ = json.NewDecoder(w.Body).Decode(&res)
	if res["x-tngl-editor"] != issuer {
		t.Errorf("editor = %v, want %s", res["x-tngl-editor"], issuer)
	}

	// wrong audience (knot DID): refused
	req = httptest.NewRequest(http.MethodPost, "/xrpc/com.atproto.repo.createRecord", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mint("did:web:forgesi.org"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong-aud write status = %d, want 401", w.Code)
	}

	// no token: refused
	req = httptest.NewRequest(http.MethodPost, "/xrpc/com.atproto.repo.createRecord", strings.NewReader(body))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated write status = %d, want 401", w.Code)
	}
}

// testVerifier resolves every DID to the knot's own key (repo docs carry
// it in production).
func testVerifier(key *btcec.PrivateKey) *atauth.Verifier {
	return &atauth.Verifier{ResolveDID: func(did string) (*atauth.DIDDoc, error) {
		return &atauth.DIDDoc{Keys: []atauth.DIDKey{{ID: did + "#k", Type: "EcdsaSecp256k1VerificationKey2019",
			Multibase: "z" + atauth.Base58Encode(key.PubKey().SerializeCompressed())}}}, nil
	}, Now: time.Now}
}

func nowPlus(seconds int64) time.Time { return time.Now().Add(time.Duration(seconds) * time.Second) }
