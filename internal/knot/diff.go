package knot

// The repo diff: a small LCS line diff over materialized trees. This is
// review-grade context for the console's proposal view, not a patch
// format — patch generation stays git's job.

import (
	"encoding/json"
	"net/http"
	"sort"
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
	gr := k.gitrepoRepo(body.Repo)
	paths, err := changedPaths(gr, body.Base, body.Head)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	oldFiles, err := gr.Materialize(body.Base)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	newFiles, err := gr.Materialize(body.Head)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
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
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
