package gitrepo

import (
	"fmt"
	"time"
)

// MergeBase returns the best common ancestor of a and b: the first commit
// reachable from b that is also an ancestor of a. Fine for fleet-shaped
// histories (short-lived agent branches off main); deep histories may
// produce an older-but-valid base.
func MergeBase(r Repo, a, b string) (string, error) {
	reachableA, err := ancestors(r, a)
	if err != nil {
		return "", err
	}
	reachableA[a] = true
	seen := map[string]bool{}
	queue := []string{b}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if reachableA[oid] {
			return oid, nil
		}
		parents, err := commitParents(r, oid)
		if err != nil {
			return "", err
		}
		queue = append(queue, parents...)
	}
	return "", fmt.Errorf("gitrepo: no common ancestor of %s and %s", a[:8], b[:8])
}

// Merge merges head into base (a branch name). Fast-forwards when base is
// an ancestor of head; otherwise three-way merges at the merge base with
// file-level granularity — a path changed on both sides in different ways
// is a conflict and the whole merge aborts with no ref movement.
func Merge(r Repo, baseBranch, head string) (string, bool, error) {
	baseOID, ok, err := r.Be.RefHead(r.ref(baseBranch))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, fmt.Errorf("gitrepo: branch %q not found", baseBranch)
	}
	if baseOID == head {
		return baseOID, true, nil // already merged
	}

	// Fast-forward: base is an ancestor of head.
	headAncestry, err := ancestors(r, head)
	if err != nil {
		return "", false, err
	}
	if headAncestry[baseOID] {
		if err := r.Be.SetRef(r.ref(baseBranch), head); err != nil {
			return "", false, err
		}
		return head, true, nil
	}

	mb, err := MergeBase(r, baseOID, head)
	if err != nil {
		return "", false, err
	}
	ours, err := r.Materialize(baseOID)
	if err != nil {
		return "", false, err
	}
	theirs, err := r.Materialize(head)
	if err != nil {
		return "", false, err
	}
	ancestor, err := r.Materialize(mb)
	if err != nil {
		return "", false, err
	}

	merged := map[string][]byte{}
	for k, v := range ours {
		merged[k] = v
	}
	conflicts := []string{}
	same := func(x, y []byte) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	for path, tb := range theirs {
		ab, inA := ancestor[path]
		ob, inO := ours[path]
		switch {
		case !inA: // new on theirs — take it unless ours also made it
			if inO && !same(ob, tb) {
				conflicts = append(conflicts, path)
			} else {
				merged[path] = tb
			}
		case !inO || same(ob, ab): // ours untouched — take theirs
			merged[path] = tb
		case same(ob, tb): // both made the same change
			// keep
		default:
			conflicts = append(conflicts, path)
		}
	}
	for path, ab := range ancestor {
		tb, inT := theirs[path]
		ob, inO := ours[path]
		if !inT { // deleted on theirs
			switch {
			case !inO || same(ob, ab): // ours untouched — delete
				delete(merged, path)
			case same(ob, tb):
				// both deleted
			default:
				conflicts = append(conflicts, path)
			}
		}
	}
	if len(conflicts) > 0 {
		return "", false, fmt.Errorf("gitrepo: merge conflicts in %v", conflicts)
	}

	tree, err := r.WriteTreeFromFiles(merged)
	if err != nil {
		return "", false, err
	}
	oid, err := r.StoreObject("commit", EncodeCommit(CommitSpec{
		Tree:    tree,
		Parents: []string{baseOID, head},
		Ident:   "ForgeSI <merge@forgesi.org>",
		When:    time.Now(),
		Message: fmt.Sprintf("Merge %s into %s", head[:10], baseBranch),
	}))
	if err != nil {
		return "", false, err
	}
	if err := r.Be.SetRef(r.ref(baseBranch), oid); err != nil {
		return "", false, err
	}
	return oid, false, nil
}

func ancestors(r Repo, start string) (map[string]bool, error) {
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		parents, err := commitParents(r, oid)
		if err != nil {
			return nil, err
		}
		queue = append(queue, parents...)
	}
	return seen, nil
}

func commitParents(r Repo, oid string) ([]string, error) {
	c, err := r.ReadObject(oid)
	if err != nil {
		return nil, err
	}
	if c.Type != "commit" {
		return nil, fmt.Errorf("gitrepo: %s is not a commit", oid[:8])
	}
	var parents []string
	lines := splitLines(c.Data)
	for _, ln := range lines {
		if !hasPrefix(ln, "parent ") {
			continue
		}
		parents = append(parents, string(ln[len("parent "):]))
	}
	return parents, nil
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func hasPrefix(b []byte, s string) bool {
	if len(b) < len(s) {
		return false
	}
	return string(b[:len(s)]) == s
}
