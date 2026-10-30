package cob

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
	"github.com/yieldenjoyoors/forgesi/internal/tid"
)

// The COB record store: Tangled's collaborative objects — issues, PRs,
// comments, labels — stored as version chains of git objects under
// refs/cobs/<collection>/<rkey>. Change ids are REAL git commit SHAs from
// day one so the Artifacts-backed backend is a drop-in later.

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(NewMemoryBackend(), "did:web:knot.forgesi.org")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateGetRoundtrip(t *testing.T) {
	s := newTestStore(t)
	rec := map[string]any{
		"$type":     "org.tangled.track.ticket",
		"title":     map[string]any{"$type": "org.tangled.markup.markdown#inline", "text": "fix the login loop"},
		"state":     "open",
		"createdAt": "2026-10-31T12:00:00.000Z",
	}
	w, err := s.Create("did:web:knot.forgesi.org:repo:demo", "org.tangled.track.ticket", "", rec, "did:plc:editor")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.URI(), "at://did:web:knot.forgesi.org:repo:demo/org.tangled.track.ticket/") {
		t.Errorf("uri = %q", w.URI())
	}
	if !tid.Valid(w.Rkey) {
		t.Errorf("minted rkey %q is not a TID", w.Rkey)
	}
	// The CID must match the canonical dag-cbor of the record — the same
	// value the reference implementation would compute.
	wantBytes, _ := dagcbor.Encode(rec)
	wantCID, _ := dagcbor.CIDString(wantBytes)
	if w.CID != wantCID {
		t.Errorf("cid = %s, want %s", w.CID, wantCID)
	}
	if len(w.Change) != 40 {
		t.Errorf("change oid %q is not a git sha", w.Change)
	}

	got, err := s.Get("did:web:knot.forgesi.org:repo:demo", "org.tangled.track.ticket", w.Rkey)
	if err != nil {
		t.Fatal(err)
	}
	if got.CID != w.CID {
		t.Errorf("get cid = %s, want %s", got.CID, w.CID)
	}
	if got.Editor != "did:plc:editor" {
		t.Errorf("editor = %q", got.Editor)
	}
	if got.Value["$type"] != "org.tangled.track.ticket" {
		t.Errorf("value = %v", got.Value)
	}
}

func TestPutSwapConflict(t *testing.T) {
	s := newTestStore(t)
	rec := map[string]any{"$type": "x.y.z", "state": "open", "createdAt": "2026-10-31T12:00:00.000Z"}
	w, _ := s.Create("did:web:r:demo", "x.y.z", "3jzfcijpj2z2a", rec, "did:plc:a")
	next := map[string]any{"$type": "x.y.z", "state": "closed", "createdAt": "2026-10-31T12:00:00.000Z"}
	// A stale swap guard (wrong cid) must refuse with InvalidSwap.
	if _, err := s.Put("did:web:r:demo", "x.y.z", w.Rkey, next, "bafyreiwrong", "did:plc:a"); err != ErrInvalidSwap {
		t.Errorf("stale swap err = %v, want ErrInvalidSwap", err)
	}
	// The correct guard passes.
	w2, err := s.Put("did:web:r:demo", "x.y.z", w.Rkey, next, w.CID, "did:plc:b")
	if err != nil {
		t.Fatal(err)
	}
	if w2.CID == w.CID {
		t.Error("put should produce a new cid")
	}
	got, _ := s.Get("did:web:r:demo", "x.y.z", w.Rkey)
	if got.Value["state"] != "closed" {
		t.Errorf("state = %v", got.Value["state"])
	}
	if got.Editor != "did:plc:b" {
		t.Errorf("last editor = %q", got.Editor)
	}
}

func TestDeleteTombstones(t *testing.T) {
	s := newTestStore(t)
	rec := map[string]any{"$type": "x.y.z", "createdAt": "2026-10-31T12:00:00.000Z"}
	w, _ := s.Create("did:web:r:demo", "x.y.z", "", rec, "did:plc:a")
	if _, err := s.Delete("did:web:r:demo", "x.y.z", w.Rkey, w.CID, "did:plc:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("did:web:r:demo", "x.y.z", w.Rkey); err != ErrNotFound {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	// Deleting a second time (stale swap) must conflict.
	if _, err := s.Delete("did:web:r:demo", "x.y.z", w.Rkey, w.CID, "did:plc:a"); err != ErrInvalidSwap {
		t.Errorf("double delete err = %v, want ErrInvalidSwap", err)
	}
}

func TestListVersionsOldestFirst(t *testing.T) {
	s := newTestStore(t)
	base := map[string]any{"$type": "x.y.z", "n": float64(1), "createdAt": "2026-10-31T12:00:00.000Z"}
	w1, _ := s.Create("did:web:r:demo", "x.y.z", "", base, "did:plc:a")
	v2 := map[string]any{"$type": "x.y.z", "n": float64(2), "createdAt": "2026-10-31T12:00:00.000Z"}
	w2, _ := s.Put("did:web:r:demo", "x.y.z", w1.Rkey, v2, w1.CID, "did:plc:a")
	v3 := map[string]any{"$type": "x.y.z", "n": float64(3), "createdAt": "2026-10-31T12:00:00.000Z"}
	w3, _ := s.Put("did:web:r:demo", "x.y.z", w1.Rkey, v3, w2.CID, "did:plc:a")

	vs, err := s.ListVersions("did:web:r:demo", "x.y.z", w1.Rkey, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("versions = %d, want 3", len(vs))
	}
	if vs[0].CID != w1.CID || vs[1].CID != w2.CID || vs[2].CID != w3.CID {
		t.Errorf("order = %s,%s,%s", vs[0].CID, vs[1].CID, vs[2].CID)
	}
	for _, v := range vs {
		if len(v.ChangeID) != 40 {
			t.Errorf("changeId %q not a git sha", v.ChangeID)
		}
		if _, err := hex.DecodeString(v.ChangeID); err != nil {
			t.Errorf("changeId %q not hex: %v", v.ChangeID, err)
		}
	}
	// Head change id must equal the latest write's change.
	if vs[2].ChangeID != w3.Change {
		t.Errorf("head changeId = %s, want %s", vs[2].ChangeID, w3.Change)
	}
}
