package artifactstore

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yieldenjoyoors/forgesi/internal/artifacts"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
	"github.com/yieldenjoyoors/forgesi/internal/packfile"
	"github.com/yieldenjoyoors/forgesi/internal/packline"
)

// minigit is a minimal smart-HTTP v0/v2 git server over a
// MemoryBackend — the shape Artifacts presents. It exists to prove the
// Backend's wire behavior (push, ls-remote, fetch) against real
// protocol flows, not a mock of our own code paths.
type minigit struct {
	mu   sync.Mutex
	be   *cob.MemoryBackend
	reqs int
}

func newMinigit(t *testing.T) *httptest.Server {
	t.Helper()
	g := &minigit{be: cob.NewMemoryBackend()}
	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			http.Error(w, "unauth", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /accounts/acct/artifacts/namespaces/ns/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":{"id":"t1","plaintext":"testtoken","scope":"write","expires_at":"2030-01-01T00:00:00Z"}}`)
	})
	mux.HandleFunc("GET /git/ns/{repo}/info/refs", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		g.reqs++
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write(packline.Pkt("# service=git-upload-pack\n"))
		_, _ = w.Write(packline.FlushPkt())
		refs, _ := g.be.ListRefs("refs/", "", 1000)
		for _, name := range refs {
			oid, _, _ := g.be.RefHead(name)
			_, _ = w.Write(packline.Pkt(oid + " " + name + "\n"))
		}
		_, _ = w.Write(packline.Pkt("0000000000000000000000000000000000000000 capabilities^{}\n"))
		_, _ = w.Write(packline.FlushPkt())
	})
	mux.HandleFunc("POST /git/ns/{repo}/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		// v2 fetch: collect wants until flush
		var wants []string
		pr := packline.NewReader(bytes.NewReader(body))
		inCmd := false
		for {
			payload, kind, err := pr.Next()
			if err != nil || kind == "flush" {
				if kind == "flush" && inCmd {
					break
				}
				if err != nil {
					break
				}
				continue
			}
			if kind != "data" {
				continue
			}
			s := strings.TrimSpace(string(payload))
			if !inCmd {
				if strings.HasPrefix(s, "command=fetch") {
					inCmd = true
				}
				continue
			}
			if want, ok := strings.CutPrefix(s, "want "); ok {
				wants = append(wants, strings.TrimSpace(want))
			}
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, w := range wants {
			if o, err := g.be.GetObject(w); err != nil {
				fmt.Fprintf(os.Stderr, "  want %s: MISSING (%v)\n", w[:8], err)
			} else {
				fmt.Fprintf(os.Stderr, "  want %s: present %d bytes\n", w[:8], len(o))
			}
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		_, _ = w.Write(packline.Pkt("packfile\n"))
		var objs []packfile.Object
		seen := map[string]bool{}
		for _, want := range wants {
			closure(g.be, want, seen, &objs)
		}
		pack, err := packfile.Encode(objs)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// sideband-64k channel 1 chunks
		for len(pack) > 0 {
			n := len(pack)
			if n > 60000 {
				n = 60000
			}
			chunk := append([]byte{1}, pack[:n]...)
			_, _ = w.Write(packline.EncodePkt(chunk))
			pack = pack[n:]
		}
		_, _ = w.Write(packline.FlushPkt())
	})
	mux.HandleFunc("POST /git/ns/{repo}/git-receive-pack", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		// v0: first pkt is "<old> <new> <ref>\0caps", then flush, then pack
		first, _, _ := packline.NewReader(bytes.NewReader(body)).Next()
		parts := strings.SplitN(strings.SplitN(string(first), "\x00", 2)[0], " ", 3)
		if len(parts) != 3 {
			http.Error(w, "bad push", 400)
			return
		}
		oldOID, newOID, ref := parts[0], parts[1], parts[2]
		// pack starts after the first pkt + the flush pkt that follows it
		firstLen := parseHexLen(body[:4])
		packBytes := body[firstLen+4:]
		objs, err := packfile.Decode(bytes.NewReader(packBytes))
		if err != nil {
			http.Error(w, "pack: "+err.Error(), 400)
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if oldOID != "0000000000000000000000000000000000000000" {
			cur, ok, _ := g.be.RefHead(ref)
			if !ok || cur != oldOID {
				_, _ = w.Write(packline.EncodePkt([]byte("unpack ok\n")))
				_, _ = w.Write(packline.EncodePkt([]byte("ng " + ref + " non-fast-forward\n")))
				_, _ = w.Write(packline.FlushPkt())
				return
			}
		}
		for _, o := range objs {
			_, _ = g.be.PutObject(packfile.GitObject(packfile.TypeName(o.Type), o.Data))
		}
		_ = g.be.SetRef(ref, newOID)
		_, _ = w.Write(packline.EncodePkt([]byte("unpack ok\n")))
		_, _ = w.Write(packline.EncodePkt([]byte("ok " + ref + "\n")))
		_, _ = w.Write(packline.FlushPkt())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func parseHexLen(b []byte) int {
	n := 0
	for _, c := range b {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = int(c-'A') + 10
		}
		n = n*16 + v
	}
	return n
}

// closure walks reachability using gitrepo helpers over the backend.
func closure(be cob.Backend, tip string, seen map[string]bool, out *[]packfile.Object) {
	if seen[tip] {
		return
	}
	seen[tip] = true
	obj, err := be.GetObject(tip)
	if err != nil {
		return
	}
	p, err := gitrepo.Parse(obj)
	if err != nil {
		return
	}
	*out = append(*out, packfile.Object{Type: packfile.TypeOf(p.Type), Data: p.Data, OID: tip})
	gr := gitrepo.Repo{Be: be}
	switch p.Type {
	case "commit":
		treeOID, parents := commitParts(p.Data)
		closure(be, treeOID, seen, out)
		for _, par := range parents {
			closure(be, par, seen, out)
		}
	case "tree":
		for _, e := range gitrepo.ParseTree(p.Data) {
			closure(be, e.OID, seen, out)
		}
	}
	_ = gr
}

func commitParts(raw []byte) (string, []string) {
	tree := ""
	var parents []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			break
		}
		if t, ok := strings.CutPrefix(line, "tree "); ok {
			tree = strings.TrimSpace(t)
		}
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			parents = append(parents, strings.TrimSpace(p))
		}
	}
	return tree, parents
}

func TestBackendRoundTrip(t *testing.T) {
	srv := newMinigit(t)
	c := artifacts.New(artifacts.Config{
		AccountID: "acct", Namespace: "ns", APIToken: "x",
		GitBase: srv.URL, RESTBase: srv.URL,
		HTTP: srv.Client(),
	})
	be := New(c, "r1")

	// build a commit through the backend like the knot would
	gr := gitrepo.Repo{Be: be}
	commit, err := gr.CommitFileOn("main", "", "hello.txt", []byte("hello artifacts\n"),
		"test: first", "t <t@forgesi>")
	if err != nil {
		t.Fatal(err)
	}
	if err := be.SetRef("refs/heads/main", commit); err != nil {
		t.Fatal("setRef:", err)
	}

	// a FRESH backend (cold cache) reads everything back through the wire
	be2 := New(c, "r1")
	oid, ok, err := be2.RefHead("refs/heads/main")
	if err != nil || !ok {
		t.Fatalf("refhead: %v %v", ok, err)
	}
	if oid != commit {
		t.Fatalf("ref mismatch: %s != %s", oid, commit)
	}
	framed, err := be2.GetObject(oid)
	if err != nil {
		t.Fatal("getobject:", err)
	}
	p, err := gitrepo.Parse(framed)
	if err != nil || p.Type != "commit" {
		t.Fatalf("parse: %v %s", err, p.Type)
	}
	gr2 := gitrepo.Repo{Be: be2}
	files, err := gr2.Materialize(commit)
	if err != nil {
		t.Fatal("materialize:", err)
	}
	if string(files["hello.txt"]) != "hello artifacts\n" {
		t.Fatalf("content mismatch: %q", files["hello.txt"])
	}

	// a second commit on top: parent closure must survive the push
	commit2, err := gr.CommitFileOn("main", "", "second.txt", []byte("two\n"), "test: second", "t <t@forgesi>")
	if err != nil {
		t.Fatal(err)
	}
	if err := be.SetRef("refs/heads/main", commit2); err != nil {
		t.Fatal("setRef2:", err)
	}
	be3 := New(c, "r1")
	files3, err := gitrepo.Repo{Be: be3}.Materialize(commit2)
	if err != nil {
		t.Fatal("materialize2:", err)
	}
	if string(files3["hello.txt"]) != "hello artifacts\n" || string(files3["second.txt"]) != "two\n" {
		t.Fatalf("second closure wrong: %v", files3)
	}

	// ListRefs honors prefix + after
	refs, err := be3.ListRefs("refs/heads/", "", 10)
	if err != nil || len(refs) != 1 || refs[0] != "refs/heads/main" {
		t.Fatalf("listrefs: %v %v", refs, err)
	}
}
