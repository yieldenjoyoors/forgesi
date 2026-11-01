package knot

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Repo action surface: file read/write with optimistic concurrency, and
// proposal merging. Same auth rule as record writes: aud = repo DID,
// editor = the token's subject (the signed-in user via PDS service-auth,
// or a minted agent token).

type fileBody struct {
	Repo     string `json:"repo"`
	Path     string `json:"path"`
	Ref      string `json:"ref"`
	Content  string `json:"content"`
	BaseHead string `json:"baseHead"`
	Branch   string `json:"branch"`
	Msg      string `json:"msg"`
}

type mergeBody struct {
	Repo string `json:"repo"`
	Base string `json:"base"`
	Head string `json:"head"`
}

// POST org.tangled.repo.getFile {repo, ref?, path} -> {content, head, ref}
func (k *Knot) handleGetFile(w http.ResponseWriter, r *http.Request) {
	var body fileBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	ref := body.Ref
	if ref == "" {
		ref = "main"
	}
	gr := k.gitrepoRepo(body.Repo)
	at := ref
	if !strings.HasPrefix(ref, "refs/") && len(ref) != 40 {
		if oid, ok, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/" + ref); ok {
			at = oid
		}
	}
	content, err := gr.ReadFileAt(at, body.Path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "FileNotFound", err.Error())
		return
	}
	head, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/" + ref)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    body.Path,
		"ref":     ref,
		"content": string(content),
		"head":    head,
	})
}

// POST org.tangled.repo.putFile {repo, path, content, baseHead?, branch?,
// msg?} -> {commit, head}. baseHead guards against lost updates: if the
// branch moved since the client read it, 409 HeadMoved carries the fresh
// head so the client can re-apply (the fast-delete discipline from the
// old forge).
func (k *Knot) handlePutFile(w http.ResponseWriter, r *http.Request) {
	var body fileBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	editor := k.opt.Auth.Authorize(r, body.Repo)
	if editor == "" {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	if body.Path == "" || strings.Contains(body.Path, "..") {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "bad path")
		return
	}
	branch := body.Branch
	if branch == "" {
		branch = "main"
	}
	gr := k.gitrepoRepo(body.Repo)
	if body.BaseHead != "" {
		head, hasHead, err := gr.Be.RefHead(gr.RefPrefix + "refs/heads/" + branch)
		if err == nil && hasHead && head != body.BaseHead {
			writeErr(w, http.StatusConflict, "HeadMoved", head)
			return
		}
	} else if branch != "main" {
		// new agent branches fork main by default
		if head, hasHead, err := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main"); err == nil && hasHead {
			if _, exists, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/" + branch); !exists {
				body.BaseHead = head
			}
		}
	}
	msg := body.Msg
	if msg == "" {
		msg = "Update " + body.Path
	}
	commit, err := gr.CommitFileOn(branch, body.BaseHead, body.Path, []byte(body.Content), msg, editor+" <"+editor+">")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	head, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/" + branch)
	k.emit(body.Repo, "commit", "", body.Path, commit, head, map[string]any{"path": body.Path})
	writeJSON(w, http.StatusOK, map[string]any{"commit": commit, "head": head})
}

// POST org.tangled.git.merge {repo, base?, head} -> {commit, fastForward}
func (k *Knot) handleMerge(w http.ResponseWriter, r *http.Request) {
	var body mergeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	editor := k.opt.Auth.Authorize(r, body.Repo)
	if editor == "" {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	if body.Base == "" {
		body.Base = "main"
	}
	gr := k.gitrepoRepo(body.Repo)
	head := body.Head
	if len(head) != 40 {
		if oid, ok, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/" + strings.TrimPrefix(head, "refs/heads/")); ok {
			head = oid
		}
	}
	commit, ff, err := mergeRepo(gr, body.Base, head)
	if err != nil {
		if strings.Contains(err.Error(), "conflicts") {
			writeErr(w, http.StatusConflict, "MergeConflict", err.Error())
			return
		}
		writeErr(w, http.StatusNotFound, "InvalidRequest", err.Error())
		return
	}
	k.emit(body.Repo, "commit", "", "refs/heads/"+body.Base, commit, commit,
		map[string]any{"merge": body.Head, "editor": editor})
	writeJSON(w, http.StatusOK, map[string]any{"commit": commit, "fastForward": ff})
}
