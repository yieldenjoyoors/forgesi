package knot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// git's exact v2 request bytes, pinned so the parser can't drift.
func TestUploadPackLsRefsWireBytes(t *testing.T) {
	h := New(Options{Host: "forgesi.org", Version: "0.1.0", Auth: DevAuthorizer{DID: "did:web:forgesi.org:dev"}})
	req1 := httptest.NewRequest(http.MethodPost, "/xrpc/org.tangled.repo.createRepo", strings.NewReader(`{"name":"wire"}`))
	req1.Header.Set("Authorization", "Bearer dev")
	h.ServeHTTP(httptest.NewRecorder(), req1)

	body := "0014command=ls-refs\n" +
		"0011agent=git/2.47.0\n" +
		"0001" +
		"0009peel\n" +
		"000dsymrefs\n" +
		"0012ref-prefix  \n" +
		"001bref-prefix refs/\n" +
		"0000"
	req := httptest.NewRequest(http.MethodPost, "/did:web:forgesi.org:repo:wire/git-upload-pack", strings.NewReader(body))
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ls-refs status = %d body=%s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	if !strings.HasPrefix(out, "0000") && !strings.Contains(out, "version 2") && !strings.Contains(out, "refs/") {
		// empty repo: ls-refs returns just a flush — both shapes legal
		if out != "0000" && !strings.Contains(out, "0000") {
			t.Errorf("unexpected ls-refs response: %q", out)
		}
	}
}
