package knot

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yieldenjoyoors/forgesi/internal/packfile"
	"github.com/yieldenjoyoors/forgesi/internal/packline"
)

func TestReceivePackWireDebug(t *testing.T) {
	h := New(Options{Host: "forgesi.org", Version: "0.1.0", Auth: DevAuthorizer{DID: "did:web:forgesi.org:dev"}})
	req1 := httptest.NewRequest(http.MethodPost, "/xrpc/org.tangled.repo.createRepo", strings.NewReader(`{"name":"rw"}`))
	req1.Header.Set("Authorization", "Bearer dev")
	h.ServeHTTP(httptest.NewRecorder(), req1)

	// build a tiny valid repo pack: blob + tree + commit
	blob := []byte("hi\n")
	blobOID := packfile.OID(packfile.GitObject("blob", blob))
	tree := []byte("100644 hi.txt\x00" + hexBytes(blobOID))
	treeOID := packfile.OID(packfile.GitObject("tree", tree))
	commit := []byte("tree " + treeOID + "\nauthor t <t@t> 1730000000 +0000\ncommitter t <t@t> 1730000000 +0000\n\nm\n")
	commitOID := packfile.OID(packfile.GitObject("commit", commit))
	pack, err := packfile.Encode([]packfile.Object{
		{Type: packfile.TypeBlob, Data: blob},
		{Type: packfile.TypeTree, Data: tree},
		{Type: packfile.TypeCommit, Data: commit},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	cmd := "0000000000000000000000000000000000000000 " + commitOID + " refs/heads/main\x00 report-status\n"
	body.Write(packline.EncodePkt([]byte(cmd)))
	body.WriteString("0000")
	body.Write(pack)

	req := httptest.NewRequest(http.MethodPost, "/did:web:forgesi.org:repo:rw/git-receive-pack", bytes.NewReader(body.Bytes()))
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	report := w.Body.String()
	if !strings.Contains(report, "unpack ok") || !strings.Contains(report, "ok refs/heads/main") {
		t.Fatalf("status report missing: %q", report)
	}
}

func hexBytes(oid string) string {
	out := make([]byte, len(oid)/2)
	for i := 0; i+1 < len(oid); i += 2 {
		out[i/2] = hexVal2(oid[i])<<4 | hexVal2(oid[i+1])
	}
	return string(out)
}

func hexVal2(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return 0
}
