package todo

import "testing"

func TestParseAndPick(t *testing.T) {
	content := `# Project todo

- [ ] fix the login loop
- [x] ship the landing page #done
- [ ] add rate limiting @julien
- [ ] refactor storage #in-progress
  detail line attached to the item above
prose without a checkbox
- [ ] write the docs #review @agent-1
`
	items := Parse(content)
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	if items[0].Title != "fix the login loop" || items[0].Assignee != "" || items[0].Status != "" {
		t.Errorf("item 0 = %+v", items[0])
	}
	if !items[1].Done {
		t.Errorf("item 1 not done")
	}
	if items[2].Assignee != "julien" || items[2].Title != "add rate limiting" {
		t.Errorf("item 2 = %+v", items[2])
	}
	if items[3].Status != "in-progress" {
		t.Errorf("item 3 = %+v", items[3])
	}
	pick, ok := Pick(items)
	if !ok || pick.Title != "fix the login loop" {
		t.Errorf("pick = %+v ok=%v", pick, ok)
	}
}

func TestPickEmptyWhenAllOwned(t *testing.T) {
	items := Parse("- [ ] a @x\n- [ ] b #review\n- [x] c\n")
	if _, ok := Pick(items); ok {
		t.Fatal("queue should be empty")
	}
}

func TestAssignWritesTagInPlace(t *testing.T) {
	content := "- [ ] fix the login loop\n- [ ] other task\n"
	got := Assign(content, 1, "agent-7", "in-progress")
	want := "- [ ] fix the login loop @agent-7 #in-progress\n- [ ] other task\n"
	if got != want {
		t.Fatalf("Assign = %q", got)
	}
	// existing tags are replaced, not duplicated
	got = Assign("- [ ] task @old #review\n", 1, "agent-2", "in-progress")
	if got != "- [ ] task @agent-2 #in-progress\n" {
		t.Fatalf("re-assign = %q", got)
	}
}
