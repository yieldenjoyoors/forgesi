package forgeci

import (
	"encoding/json"
	"path"
	"strings"
)

// ForgeCI is the repo-level agent policy file (forgeci.json). It tells
// the fleet when a proposal can be cleared by the machine review alone
// and when a human must approve the merge.
//
//	{
//	  "review": {
//	    "default": "human",                        // when no rule matches
//	    "agent":   [{ "path": "docs/**" }, { "path": "*.md" }],
//	    "human":   [{ "path": "internal/**" }, { "path": "*.go" }]
//	  }
//	}
//
// Evaluation walks the changed files of a proposal: any file matching a
// human rule requires a human, regardless of other matches; otherwise a
// file matching an agent rule is cleared; otherwise the default applies.

type ForgeCI struct {
	Review Review `json:"review"`
}

type Review struct {
	Default string     `json:"default"`
	Agent   []PathRule `json:"agent"`
	Human   []PathRule `json:"human"`
}

type PathRule struct {
	Path string `json:"path"`
}

// Decision is the verdict of the policy for one proposal.
type Decision struct {
	HumanRequired bool
	Reason        string
}

// Parse reads forgeci.json. An empty file (or absent) yields the safe
// default: every proposal needs a human.
func Parse(content []byte) (*ForgeCI, error) {
	f := &ForgeCI{}
	if len(strings.TrimSpace(string(content))) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(content, f); err != nil {
		return nil, err
	}
	return f, nil
}

// Evaluate decides for a set of changed file paths.
func (f *ForgeCI) Evaluate(changed []string) Decision {
	for _, p := range f.Review.Human {
		for _, c := range changed {
			if matchPath(p.Path, c) {
				return Decision{HumanRequired: true, Reason: c + " matches human rule " + p.Path}
			}
		}
	}
	cleared := []string{}
	for _, c := range changed {
		matched := false
		for _, p := range f.Review.Agent {
			if matchPath(p.Path, c) {
				matched = true
				break
			}
		}
		if !matched {
			cleared = append(cleared, c)
		}
	}
	if len(cleared) == 0 && len(changed) > 0 {
		return Decision{HumanRequired: false, Reason: "all changed files match agent rules"}
	}
	def := f.Review.Default
	if def == "" {
		def = "human" // safe default
	}
	if def == "agent" {
		return Decision{HumanRequired: false, Reason: "default: agent review"}
	}
	if len(cleared) > 0 {
		return Decision{HumanRequired: true, Reason: "unmatched: " + strings.Join(cleared, ", ")}
	}
	return Decision{HumanRequired: true, Reason: "default is human"}
}

// matchPath: "dir/**" matches everything under dir (inclusive); other
// patterns use path.Match against the full path and its base name.
func matchPath(pattern, p string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "**")
		return strings.HasPrefix(p, prefix)
	}
	if ok, err := path.Match(pattern, p); err == nil && ok {
		return true
	}
	ok, err := path.Match(pattern, path.Base(p))
	return err == nil && ok
}
