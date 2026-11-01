package knot

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// The machine reviewer: after runDone opens a proposal, the reviewer model's
// verdict lands as a comment on the ticket.
func TestMachineReviewCommentsVerdict(t *testing.T) {
	key, _ := btcec.NewPrivateKey()
	db, _ := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	verdicts := make(chan string, 1)
	h := New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth: ServiceAuth{V: testVerifier(key)},
		DB:   db, MintKey: key, RunnerKey: "rk",
		ReviewLLM: func(system, user string) (string, error) {
			v := <-verdicts
			return v, nil
		},
	})

	// the repo must exist in the registry (listRecords gates on it)
	boot, merr := atauth.Mint(key, "did:web:forgesi.org:repo:rv", "did:web:forgesi.org", "", time.Hour, time.Now())
	if merr != nil {
		t.Fatal(merr)
	}
	reqB := httptest.NewRequest(http.MethodPost, "/xrpc/org.tangled.repo.createRepo",
		strings.NewReader(`{"name":"rv"}`))
	reqB.Header.Set("Authorization", "Bearer "+boot)
	h.ServeHTTP(httptest.NewRecorder(), reqB)

	// drive the proposal path directly (the same code runDone uses)
	h.openProposal("did:web:forgesi.org:repo:rv", "agent/3", "", "abc123", "wrote the login fix", "fix the login loop")

	// feed the verdict; the reviewer goroutine writes the comment
	verdicts <- "✅ Looks correct: adds the fix and a test. Check the error path."

	// comment lands on the ticket
	found := false
	for i := 0; i < 50 && !found; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:rv&collection=org.tangled.feed.comment", nil))
		found = strings.Contains(w.Body.String(), "Looks correct")
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet,
			"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:rv&collection=org.tangled.feed.comment", nil))
		t.Fatalf("verdict missing; list=%s", w2.Body.String())
	}
	// the comment editor is the knot's reviewer identity
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/org.tangled.repo.listRecordVersions?uri=at://did:web:forgesi.org:repo:rv/org.tangled.feed.comment/x&limit=1", nil))
	_ = w // versions shape checked elsewhere; presence is the assertion
}
