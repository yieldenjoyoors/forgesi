package todo

import (
	"strings"
	"testing"
)

func TestComplete(t *testing.T) {
	content := "# todo\n\n- [ ] write the mission statement\n- [x] set up the repo\n- [ ] second task (in-progress @agent-1 #review)\n"
	out := Complete(content, "write the mission statement")
	items := Parse(out)
	if !items[0].Done {
		t.Fatalf("item not marked done: %q", out)
	}
	if items[0].Assignee != "" || items[0].Status != "" {
		t.Fatalf("tags should be stripped on completion: %+v", items[0])
	}
	// untouched lines stay put
	if items[1].Title != "set up the repo" || !items[1].Done {
		t.Fatalf("unrelated item changed: %+v", items[1])
	}
	if items[2].Done {
		t.Fatal("other open item was flipped")
	}
}

func TestCompleteTagged(t *testing.T) {
	// the assigned shape the orchestrator writes
	content := "- [ ] write the mission statement @agent-1 #in-progress\n"
	out := Complete(content, "write the mission statement")
	if strings.TrimSpace(out) != "- [x] write the mission statement" {
		t.Fatalf("completed line: %q", out)
	}
	if strings.Contains(out, "@agent-1") || strings.Contains(out, "#in-progress") {
		t.Fatalf("tags not stripped: %q", out)
	}
}

func TestCompleteMissing(t *testing.T) {
	out := Complete("- [ ] something else\n", "no such task")
	if strings.Contains(out, "[x]") {
		t.Fatal("flipped a line that does not match")
	}
}
