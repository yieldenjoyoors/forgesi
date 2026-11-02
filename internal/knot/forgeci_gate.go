package knot

// The forgeci.json merge gate. A repo can carry a forgeci.json telling
// the fleet when the machine review alone clears a proposal and when a
// human must approve. The knot itself (orchestrator, reviewer, or a
// self-minted merge) may only land proposals the policy clears; humans
// merge anything — they ARE the approval. Absent or invalid policy =
// human required, the safe default.

import (
	"sort"
	"strings"

	"github.com/yieldenjoyoors/forgesi/internal/forgeci"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
)

// selfDID is the knot's own editor identity — the authority the fleet
// acts under (auto-merge after a clear review, todo flips on merge).
func (k *Knot) selfDID() string {
	return "did:web:" + k.opt.Host
}

// isFleetEditor: the editor acts on the knot's authority rather than a
// human's. The orchestrator and reviewer identities are derived from the
// knot DID by suffix; probes and auto-merges use the knot DID itself.
func (k *Knot) isFleetEditor(editor string) bool {
	self := k.selfDID()
	return editor == self ||
		editor == self+":orchestrator" ||
		editor == self+":reviewer" ||
		strings.HasSuffix(editor, ":agent")
}

// changedPaths lists the files that differ between the base and head
// trees of a proposal.
func changedPaths(gr gitrepo.Repo, base, head string) ([]string, error) {
	oldFiles, err := gr.Materialize(base)
	if err != nil {
		return nil, err
	}
	newFiles, err := gr.Materialize(head)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for p := range oldFiles {
		if _, ok := newFiles[p]; !ok {
			seen[p] = true
			out = append(out, p)
		}
	}
	for p, nf := range newFiles {
		if old, ok := oldFiles[p]; !ok || string(old) != string(nf) {
			if !seen[p] {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// policyFor loads forgeci.json at the given commit. Absent or invalid
// content yields the zero policy: every proposal needs a human.
func policyFor(gr gitrepo.Repo, at string) *forgeci.ForgeCI {
	raw, err := gr.ReadFileAt(at, "forgeci.json")
	if err != nil {
		return &forgeci.ForgeCI{}
	}
	f, perr := forgeci.Parse(raw)
	if perr != nil {
		return &forgeci.ForgeCI{}
	}
	return f
}

// policyDecision evaluates the proposal's changed files against the
// repo's forgeci.json. Any evaluation failure is human-required.
func policyDecision(gr gitrepo.Repo, base, head string) forgeci.Decision {
	changed, err := changedPaths(gr, base, head)
	if err != nil {
		return forgeci.Decision{HumanRequired: true, Reason: "diff unavailable: " + err.Error()}
	}
	f := policyFor(gr, base)
	return f.Evaluate(changed)
}
