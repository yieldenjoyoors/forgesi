package packfile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitProducedPack builds a REAL pack with the git CLI: a repo with enough
// files and history for git to emit delta objects.
func gitProducedPack(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for i := 0; i < 3; i++ {
		big := make([]byte, 4096)
		for j := range big {
			big[j] = byte('a' + (j+i)%26)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.txt"), big, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte("file "+string(rune('0'+i))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "c"+string(rune('0'+i)))
	}
	// pack everything: pack-objects reads the object list from stdin
	cmd := exec.Command("sh", "-c", "cd '"+dir+"' && git rev-list --objects --all | git pack-objects --stdout")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pack-objects: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty pack")
	}
	return out
}
