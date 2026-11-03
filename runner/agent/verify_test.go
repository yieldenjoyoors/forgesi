package agent

import "testing"

// CI-lite: the verifier checks only the files the agent touched, using
// their final merged content; anything without an in-process check
// (e.g. markdown) passes silently.
func TestVerifyChanges(t *testing.T) {
	touched := map[string][]byte{
		"good.go":     []byte("package x\n\nfunc F() {}\n"),
		"bad.go":      []byte("package x\n\nfunc ("),
		"data.json":   []byte(`{"a":1}`),
		"broken.json": []byte("{nope"),
		"notes.md":    []byte("# hi"),
	}
	merged := map[string][]byte{}
	for k, v := range touched {
		merged[k] = v
	}
	checks := verifyChanges(touched, merged)
	if len(checks) != 4 {
		t.Fatalf("checks = %d (%v), want 4 — markdown carries no check", len(checks), checks)
	}
	byName := map[string]bool{}
	for _, c := range checks {
		byName[c.Name] = c.OK
	}
	if !byName["go-syntax:good.go"] {
		t.Errorf("valid Go flagged: %+v", checks)
	}
	if byName["go-syntax:bad.go"] {
		t.Errorf("broken Go passed: %+v", checks)
	}
	if !byName["json:data.json"] {
		t.Errorf("valid JSON flagged: %+v", checks)
	}
	if byName["json:broken.json"] {
		t.Errorf("broken JSON passed: %+v", checks)
	}
}

// the merged content is what counts: an intermediate broken copy in the
// touched map must not fail when the final content is valid.
func TestVerifyUsesMergedContent(t *testing.T) {
	touched := map[string][]byte{"a.go": []byte("package x\nfunc broken(")}
	merged := map[string][]byte{"a.go": []byte("package x\n\nfunc Fixed() {}\n")}
	checks := verifyChanges(touched, merged)
	if !checksOK(checks) {
		t.Fatalf("merged content should be checked, not the stale copy: %+v", checks)
	}
}
