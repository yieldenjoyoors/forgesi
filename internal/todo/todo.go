// Package todo parses todo.md — the flat task tracker at the top of the
// automation funnel. One item per line; the orchestrator's pick queue is
// every unassigned, not-done item.
package todo

import (
	"regexp"
	"strings"
)

// Item is one todo line.
type Item struct {
	Line     int    // 1-based line in the file
	Title    string // text without tags
	Assignee string // @name, '' when unassigned
	Status   string // '' (todo) | todo | in-progress | review | blocked | done
	Done     bool   // from the checkbox
}

var itemRe = regexp.MustCompile(`^- \[([ xX])\] (.*)$`)

// known statuses; anything else after # is not a tag.
var statuses = map[string]bool{
	"todo": true, "in-progress": true, "review": true, "blocked": true, "done": true,
}

// Parse extracts items; headings, details and prose are skipped.
func Parse(content string) []Item {
	var items []Item
	for i, raw := range strings.Split(content, "\n") {
		m := itemRe.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		it := Item{Line: i + 1, Title: m[2]}
		if m[1] != " " {
			it.Done = true
		}
		// split tags off the title
		fields := strings.Fields(it.Title)
		var keep []string
		for _, f := range fields {
			switch {
			case strings.HasPrefix(f, "@"):
				it.Assignee = strings.TrimPrefix(f, "@")
			case strings.HasPrefix(f, "#"):
				st := strings.TrimPrefix(f, "#")
				if statuses[st] {
					it.Status = st
				} else {
					keep = append(keep, f)
				}
			default:
				keep = append(keep, f)
			}
		}
		it.Title = strings.Join(keep, " ")
		items = append(items, it)
	}
	return items
}

// Pick is the orchestrator's queue head: the first item that is neither
// done nor already owned.
func Pick(items []Item) (Item, bool) {
	for _, it := range items {
		if it.Done {
			continue
		}
		switch it.Status {
		case "done", "review", "in-progress", "blocked":
			continue
		}
		if it.Assignee != "" {
			continue
		}
		return it, true
	}
	return Item{}, false
}

// Assign renders the item's line with an assignee tag and status written
// back into the original content (the orchestrator's attributed commit).
func Assign(content string, line int, assignee, status string) string {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return content
	}
	m := itemRe.FindStringSubmatch(lines[line-1])
	if m == nil {
		return content
	}
	title := strings.TrimRight(m[2], " ")
	// strip existing tags of the kinds we own
	fields := strings.Fields(title)
	var keep []string
	for _, f := range fields {
		if strings.HasPrefix(f, "@") || (strings.HasPrefix(f, "#") && statuses[strings.TrimPrefix(f, "#")]) {
			continue
		}
		keep = append(keep, f)
	}
	box := "[ ]"
	if m[1] != " " {
		box = "[" + m[1] + "]"
	}
	newTitle := strings.Join(keep, " ") + " @" + assignee
	if status != "" {
		newTitle += " #" + status
	}
	lines[line-1] = "- " + box + " " + strings.TrimSpace(newTitle)
	return strings.Join(lines, "\n")
}
