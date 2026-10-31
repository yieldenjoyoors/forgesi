package cob

import (
	"database/sql"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

// The D1 backend must satisfy the same contract as memory — and survive a
// "restart" (reopen), which is the whole point: Workers isolates are
// ephemeral, D1 is not.

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestD1BackendPersistsAcrossReopen(t *testing.T) {
	db := openTestDB(t)
	be := NewD1Backend(db)

	oid, err := be.PutObject([]byte("blob 4\x00test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.SetRef("refs/cobs/x/1", oid); err != nil {
		t.Fatal(err)
	}

	// A fresh backend over the SAME database = a new isolate.
	be2 := NewD1Backend(db)
	got, err := be2.GetObject(oid)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "blob 4\x00test" {
		t.Errorf("content = %q", got)
	}
	head, ok, err := be2.RefHead("refs/cobs/x/1")
	if err != nil || !ok || head != oid {
		t.Errorf("head = %q ok=%v err=%v", head, ok, err)
	}
}

func TestStoreOverD1CreateGetAcrossBackends(t *testing.T) {
	db := openTestDB(t)
	s1, err := New(NewD1Backend(db), "did:web:knot.forgesi.org")
	if err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{"$type": "x.y.z", "state": "open", "createdAt": "2026-10-31T12:00:00.000Z"}
	w, err := s1.Create("did:web:r:d", "x.y.z", "", rec, "did:plc:a")
	if err != nil {
		t.Fatal(err)
	}

	// New isolate, same store state: the write must be visible.
	s2, err := New(NewD1Backend(db), "did:web:knot.forgesi.org")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get("did:web:r:d", "x.y.z", w.Rkey)
	if err != nil {
		t.Fatal(err)
	}
	if got.CID != w.CID || got.Value["state"] != "open" {
		t.Errorf("got %+v", got)
	}
}
