package agent

import (
	"strings"
	"testing"
)

// the repo drives the agent: AGENTS.md always lands in the system
// prompt, workflow.md customizes behavior, skills load on mention.
func TestReadAgentContext(t *testing.T) {
	files := map[string][]byte{
		"AGENTS.md":                     []byte("Always write terse doc comments."),
		".agents/workflow.md":           []byte("Work in small steps; verify before done."),
		".agents/skills/release.md":     []byte("# release\n\nBump versions like so..."),
		".agents/skills/audit/SKILL.md": []byte("# audit\n\nCheck every import."),
		"main.go":                       []byte("package main"),
		".agents/notes.txt":             []byte("not a skill"),
	}
	ac := readAgentContext(files)

	sys := ac.systemFor()
	for _, want := range []string{
		"Reply ONLY with a JSON object", // wire contract intact
		"Repo instructions (AGENTS.md)",
		"Always write terse doc comments.",
		"Repo agent workflow",
		"Work in small steps",
		"release", "audit", // skill index
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}

	task := ac.taskFor("use the release skill to cut v2")
	if !strings.Contains(task, "Bump versions like so") {
		t.Errorf("mentioned skill not loaded: %s", task)
	}
	task = ac.taskFor("unrelated task")
	if strings.Contains(task, "Bump versions") {
		t.Errorf("unmentioned skill leaked into the task")
	}
}

// empty repo: defaults, no crashes
func TestReadAgentContextEmpty(t *testing.T) {
	ac := readAgentContext(map[string][]byte{"README.md": []byte("hi")})
	if ac.systemFor() != systemPrompt {
		t.Errorf("no-repo system should equal the base prompt")
	}
	if ac.taskFor("do things") != "do things" {
		t.Errorf("task should pass through untouched")
	}
}

// gofmt is CI: unformatted Go gets formatted into the merged tree,
// exactly like the repo's own gofmt -w gate.
func TestGofmtAllFormatsTouchedGo(t *testing.T) {
	unformatted := []byte("package x\n\nfunc  F( ) {}\n")
	touched := map[string][]byte{"a.go": unformatted}
	merged := map[string][]byte{"a.go": unformatted}
	n := gofmtAll(touched, merged)
	if n != 1 {
		t.Fatalf("gofmtAll formatted %d files, want 1", n)
	}
	if string(merged["a.go"]) == string(unformatted) {
		t.Fatalf("merged content not formatted: %q", merged["a.go"])
	}
	if !strings.HasPrefix(string(merged["a.go"]), "package x") {
		t.Fatalf("formatting broke the file: %q", merged["a.go"])
	}
	// broken Go is left alone for the parse check to flag
	broken := map[string][]byte{"b.go": []byte("package x\nfunc (")}
	mb := map[string][]byte{"b.go": []byte("package x\nfunc (")}
	if gofmtAll(broken, mb) != 0 {
		t.Fatalf("broken file should not be silently touched")
	}
}
