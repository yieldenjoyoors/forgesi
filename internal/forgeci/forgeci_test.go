package forgeci

import (
	"strings"
	"testing"
)

const doc = `{
  "review": {
    "default": "human",
    "agent":   [{ "path": "docs/**" }, { "path": "*.md" }],
    "human":   [{ "path": "internal/**" }, { "path": "*.go" }]
  }
}`

func TestParseAndEvaluate(t *testing.T) {
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	d := f.Evaluate([]string{"docs/notes.md"})
	if d.HumanRequired {
		t.Fatalf("docs should be agent-clear: %+v", d)
	}
	d = f.Evaluate([]string{"README.md"})
	if d.HumanRequired {
		t.Fatalf("*.md should be agent-clear: %+v", d)
	}
	d = f.Evaluate([]string{"internal/knot/fleet.go"})
	if !d.HumanRequired {
		t.Fatalf("internal/*.go needs a human: %+v", d)
	}
	if !strings.Contains(d.Reason, "fleet.go") {
		t.Fatalf("reason should name the file: %+v", d)
	}
	// mixed: the human rule wins
	d = f.Evaluate([]string{"README.md", "main.go"})
	if !d.HumanRequired {
		t.Fatal("mixed set must require a human")
	}
	// unmatched -> default (human)
	d = f.Evaluate([]string{"some/other.txt"})
	if !d.HumanRequired {
		t.Fatal("unmatched must fall back to the default")
	}
}

func TestParseDefaultAgent(t *testing.T) {
	f, err := Parse([]byte(`{"review": {"default": "agent"}}`))
	if err != nil {
		t.Fatal(err)
	}
	d := f.Evaluate([]string{"anything/else.txt"})
	if d.HumanRequired {
		t.Fatalf("default agent should clear: %+v", d)
	}
}

func TestParseEmpty(t *testing.T) {
	f, err := Parse([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Evaluate([]string{"anything"}).HumanRequired {
		t.Fatal("absent policy = human required (safe default)")
	}
}

func TestParseBad(t *testing.T) {
	if _, err := Parse([]byte("{nope")); err == nil {
		t.Fatal("bad json should error")
	}
}
