package gitrepo

import (
	"strings"
	"testing"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

func newMergeTestRepo(t *testing.T) Repo {
	t.Helper()
	return Repo{Be: cob.NewMemoryBackend(), RefPrefix: "git/test/"}
}

func mustCommitFiles(t *testing.T, r Repo, parent string, files map[string][]byte, ident string) string {
	t.Helper()
	tree, err := r.WriteTreeFromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	var parents []string
	if parent != "" {
		parents = append(parents, parent)
	}
	oid, err := r.StoreObject("commit", EncodeCommit(CommitSpec{
		Tree: tree, Parents: parents, Ident: ident, When: time.Unix(1700000000, 0), Message: "wip",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if parent == "" {
		if err := r.Be.SetRef(r.ref("main"), oid); err != nil {
			t.Fatal(err)
		}
	}
	return oid
}

func seedBase(t *testing.T) (Repo, string, map[string][]byte) {
	t.Helper()
	r := newMergeTestRepo(t)
	base := map[string][]byte{
		"todo.md":   []byte("- [ ] task one\n- [ ] task two\n"),
		"src/a.txt": []byte("alpha\n"),
	}
	oid := mustCommitFiles(t, r, "", base, "Tester <t@x>")
	return r, oid, base
}

func TestMergeFastForward(t *testing.T) {
	r, base, _ := seedBase(t)
	branch := map[string][]byte{
		"todo.md":   []byte("- [x] task one\n- [ ] task two\n"),
		"src/a.txt": []byte("alpha\n"),
	}
	head := mustCommitFiles(t, r, base, branch, "Agent <a@x>")

	oid, ff, err := Merge(r, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	if !ff {
		t.Fatalf("expected fast-forward, got merge commit %s", oid)
	}
	if oid != head {
		t.Fatalf("fast-forward landed on %s, want %s", oid, head)
	}
	branches, _ := r.Branches()
	if branches["main"] != head {
		t.Fatalf("main = %s, want %s", branches["main"], head)
	}
}

func TestMergeThreeWayClean(t *testing.T) {
	r, base, baseFiles := seedBase(t)
	ours := cloneMap(baseFiles)
	ours["notes.md"] = []byte("added on main\n")
	main := mustCommitFiles(t, r, base, ours, "Main <m@x>")
	if err := r.Be.SetRef(r.ref("main"), main); err != nil {
		t.Fatal(err)
	}

	theirs := cloneMap(baseFiles)
	theirs["todo.md"] = []byte("- [x] task one\n- [x] task two\n")
	head := mustCommitFiles(t, r, base, theirs, "Agent <a@x>")

	oid, ff, err := Merge(r, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	if ff {
		t.Fatal("expected a real merge commit, got fast-forward")
	}
	files, err := r.Materialize(oid)
	if err != nil {
		t.Fatal(err)
	}
	if string(files["notes.md"]) != "added on main\n" {
		t.Fatalf("main-side change lost: %q", files["notes.md"])
	}
	if string(files["todo.md"]) != "- [x] task one\n- [x] task two\n" {
		t.Fatalf("branch-side change lost: %q", files["todo.md"])
	}
	if string(files["src/a.txt"]) != "alpha\n" {
		t.Fatalf("untouched file lost: %q", files["src/a.txt"])
	}
	c, err := r.ReadObject(oid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(c.Data), "parent ") != 2 {
		t.Fatalf("merge commit parents: %q", string(c.Data))
	}
}

func TestMergeConflict(t *testing.T) {
	r, base, baseFiles := seedBase(t)
	ours := cloneMap(baseFiles)
	ours["todo.md"] = []byte("- [ ] task one edited on main\n")
	main := mustCommitFiles(t, r, base, ours, "Main <m@x>")
	if err := r.Be.SetRef(r.ref("main"), main); err != nil {
		t.Fatal(err)
	}

	theirs := cloneMap(baseFiles)
	theirs["todo.md"] = []byte("- [x] task one edited on branch\n")
	head := mustCommitFiles(t, r, base, theirs, "Agent <a@x>")

	if _, _, err := Merge(r, "main", head); err == nil {
		t.Fatal("expected conflict error")
	} else if !strings.Contains(err.Error(), "todo.md") {
		t.Fatalf("conflict should name the file: %v", err)
	}
}

func TestMergeBase(t *testing.T) {
	r, base, baseFiles := seedBase(t)
	ours := cloneMap(baseFiles)
	ours["x"] = []byte("x")
	main := mustCommitFiles(t, r, base, ours, "M <m@x>")
	theirs := cloneMap(baseFiles)
	theirs["y"] = []byte("y")
	fork := mustCommitFiles(t, r, base, theirs, "A <a@x>")

	mb, err := MergeBase(r, main, fork)
	if err != nil {
		t.Fatal(err)
	}
	if mb != base {
		t.Fatalf("merge-base = %s, want %s", mb, base)
	}
}

func cloneMap(m map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range m {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
