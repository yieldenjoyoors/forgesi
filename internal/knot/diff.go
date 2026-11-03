package knot

// The repo diff: a small LCS line diff over materialized trees. This is
// review-grade context for the console's proposal view, not a patch
// format — patch generation stays git's job.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// diffLine is one output row: ' ', '+' or '-'.
type diffLine struct {
	T string `json:"t"`
	S string `json:"s"`
}

// diffFile is one changed path with its line-level diff.
type diffFile struct {
	Path   string     `json:"path"`
	Status string     `json:"status"` // added | removed | modified
	Lines  []diffLine `json:"lines"`
}

const diffMaxLines = 4000 // per side; larger files diff as "too large"

// lineDiff walks an LCS table and emits rows oldest-first.
func lineDiff(oldText, newText string) []diffLine {
	old := splitLines(oldText)
	new := splitLines(newText)
	if len(old) > diffMaxLines || len(new) > diffMaxLines {
		return []diffLine{{T: " ", S: "(file too large to diff — open it in the files pane)"}}
	}
	// lcs[i][j]: LCS length of old[i:], new[j:]
	lcs := make([][]int, len(old)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(new)+1)
	}
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(new) - 1; j >= 0; j-- {
			if old[i] == new[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []diffLine
	emit := func(t byte, s string) { out = append(out, diffLine{T: string(t), S: s}) }
	i, j := 0, 0
	for i < len(old) && j < len(new) {
		switch {
		case old[i] == new[j]:
			emit(' ', old[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			emit('-', old[i])
			i++
		default:
			emit('+', new[j])
			j++
		}
	}
	for ; i < len(old); i++ {
		emit('-', old[i])
	}
	for ; j < len(new); j++ {
		emit('+', new[j])
	}
	return out
}

func splitLines(s string) []string {
	out := []string{}
	start := 0
	for k := 0; k < len(s); k++ {
		if s[k] == '\n' {
			out = append(out, s[start:k])
			start = k + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// POST org.tangled.repo.diff {repo, base, head} -> {files: [...]}
// Public read like getFile; base/head are commit oids (a patch record
// carries them). Only the changed paths are returned.
func (k *Knot) handleDiff(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
		Base string `json:"base"`
		Head string `json:"head"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	if len(body.Base) != 40 || len(body.Head) != 40 {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "base and head must be commit oids")
		return
	}
	files := treeDiff(k, body.Repo, body.Base, body.Head)
	if files == nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", "diff failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// treeDiff materializes both commits and diffs every changed path.
// nil means the diff could not be produced.
func treeDiff(k *Knot, repoDID, base, head string) []diffFile {
	gr := k.gitrepoRepo(repoDID)
	paths, err := changedPaths(gr, base, head)
	if err != nil {
		return nil
	}
	var oldFiles, newFiles map[string][]byte
	if base != "" {
		if oldFiles, err = gr.Materialize(base); err != nil {
			return nil
		}
	}
	if newFiles, err = gr.Materialize(head); err != nil {
		return nil
	}
	files := []diffFile{}
	for _, p := range paths {
		df := diffFile{Path: p, Status: "modified", Lines: nil}
		oldC, hasOld := oldFiles[p]
		newC, hasNew := newFiles[p]
		switch {
		case !hasOld:
			df.Status = "added"
		case !hasNew:
			df.Status = "removed"
		}
		df.Lines = lineDiff(string(oldC), string(newC))
		files = append(files, df)
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return files
}

// POST org.tangled.repo.commitInfo {repo, oid} -> {oid, message,
// parents, files}: one commit opened up — its message and the diff
// against its first parent (all-added for a root commit). The console's
// clickable rows land here.
func (k *Knot) handleCommitInfo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
		OID  string `json:"oid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	if len(body.OID) != 40 {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "oid must be a commit id")
		return
	}
	obj, err := k.gitrepoRepo(body.Repo).ReadObject(body.OID)
	if err != nil || obj.Type != "commit" {
		writeErr(w, http.StatusNotFound, "NotFound", "no such commit")
		return
	}
	msg := commitMessage(obj.Data)
	parents := commitParents(obj.Data)
	base := ""
	if len(parents) > 0 {
		base = parents[0]
	}
	files := treeDiff(k, body.Repo, base, body.OID)
	if files == nil {
		files = []diffFile{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"oid": body.OID, "message": msg, "parents": parents, "files": files,
	})
}

// git commit bodies: header lines, blank line, then the message.
func commitMessage(data []byte) string {
	parts := strings.SplitN(string(data), "\n\n", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func commitParents(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "parent ") {
			out = append(out, strings.TrimPrefix(line, "parent "))
			continue
		}
		if line == "" {
			break
		}
	}
	return out
}
