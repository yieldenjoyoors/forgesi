package knot

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// Authorizer decides who is calling a write endpoint. The real verifier
// (atproto service-auth JWTs: iss=PDS, secp256k1 signature, aud/lxm/exp)
// lands as another Authorizer; handlers never change.
type Authorizer interface {
	// Authorize returns the caller's DID, or "" when unauthenticated.
	Authorize(r *http.Request) string
}

// DevAuthorizer accepts any Bearer token and attributes the write to a
// fixed DID. Local development and tests only — never wire in production.
type DevAuthorizer struct{ DID string }

// Authorize implements Authorizer.
func (d DevAuthorizer) Authorize(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return d.DID
}

// repoAccount is one repo hosted by the knot.
type repoAccount struct {
	Did, Name, DefaultBranch string
}

// repoRegistry tracks hosted repos (D1-backed in production; the seam is
// the interface, not the storage).
type repoRegistry struct {
	mu    sync.Mutex
	repos map[string]*repoAccount
}

func newRepoRegistry() *repoRegistry {
	return &repoRegistry{repos: map[string]*repoAccount{}}
}

func (rr *repoRegistry) create(did, name, branch string) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if _, ok := rr.repos[name]; ok {
		return false
	}
	rr.repos[name] = &repoAccount{Did: did, Name: name, DefaultBranch: branch}
	return true
}

func (rr *repoRegistry) byDID(did string) (*repoAccount, bool) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for _, r := range rr.repos {
		if r.Did == did {
			return r, true
		}
	}
	return nil, false
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// records wires the COB record endpoints onto the knot. Called from
// routes(); storage and auth are the Option seams.
func (k *Knot) records() {
	k.mux.HandleFunc("POST /xrpc/org.tangled.repo.createRepo", k.handleCreateRepo)
	k.mux.HandleFunc("POST /xrpc/com.atproto.repo.createRecord", k.handleCreateRecord)
	k.mux.HandleFunc("POST /xrpc/com.atproto.repo.putRecord", k.handlePutRecord)
	k.mux.HandleFunc("POST /xrpc/com.atproto.repo.deleteRecord", k.handleDeleteRecord)
	k.mux.HandleFunc("GET /xrpc/com.atproto.repo.getRecord", k.handleGetRecord)
	k.mux.HandleFunc("GET /xrpc/org.tangled.repo.listRecordVersions", k.handleListRecordVersions)
}

// atproto error envelope: {"error": "<code>", "message": "<detail>"}.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func cobWriteResult(w cob.Write) map[string]any {
	return map[string]any{
		"uri":           w.URI(),
		"cid":           w.CID,
		"x-tngl-editor": w.Editor,
		"x-tngl-change": w.Change,
	}
}

type repoBody struct {
	Repo       string         `json:"repo"`
	Collection string         `json:"collection"`
	Rkey       string         `json:"rkey"`
	Record     map[string]any `json:"record"`
	SwapRecord string         `json:"swapRecord"`
}

func (k *Knot) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	editor := k.opt.Auth.Authorize(r)
	if editor == "" {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return
	}
	var body struct {
		Name          string `json:"name"`
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	if !nameRe.MatchString(body.Name) {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "bad repo name")
		return
	}
	if body.DefaultBranch == "" {
		body.DefaultBranch = "main"
	}
	did := "did:web:" + k.opt.Host + ":repo:" + body.Name
	if !k.registry.create(did, body.Name, body.DefaultBranch) {
		writeErr(w, http.StatusConflict, "InvalidSwap", "repo already exists")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"did": did, "key": k.opt.PublicKeyMultibase})
}

func (k *Knot) authorized(w http.ResponseWriter, r *http.Request) (string, repoBody, bool) {
	editor := k.opt.Auth.Authorize(r)
	if editor == "" {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return "", repoBody{}, false
	}
	var body repoBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return "", body, false
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return "", body, false
	}
	return editor, body, true
}

func (k *Knot) handleCreateRecord(w http.ResponseWriter, r *http.Request) {
	editor, body, ok := k.authorized(w, r)
	if !ok {
		return
	}
	res, err := k.store.Create(body.Repo, body.Collection, body.Rkey, body.Record, editor)
	k.writeCobResult(w, res, err)
}

func (k *Knot) handlePutRecord(w http.ResponseWriter, r *http.Request) {
	editor, body, ok := k.authorized(w, r)
	if !ok {
		return
	}
	res, err := k.store.Put(body.Repo, body.Collection, body.Rkey, body.Record, body.SwapRecord, editor)
	k.writeCobResult(w, res, err)
}

func (k *Knot) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	editor, body, ok := k.authorized(w, r)
	if !ok {
		return
	}
	res, err := k.store.Delete(body.Repo, body.Collection, body.Rkey, body.SwapRecord, editor)
	k.writeCobResult(w, res, err)
}

func (k *Knot) writeCobResult(w http.ResponseWriter, res cob.Write, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, cobWriteResult(res))
	case errors.Is(err, cob.ErrInvalidSwap):
		writeErr(w, http.StatusConflict, "InvalidSwap", err.Error())
	case errors.Is(err, cob.ErrNotFound):
		writeErr(w, http.StatusNotFound, "RecordNotFound", err.Error())
	case errors.Is(err, cob.ErrBadRequest):
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
	}
}

func (k *Knot) handleGetRecord(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rec, err := k.store.Get(q.Get("repo"), q.Get("collection"), q.Get("rkey"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"uri":   "at://" + q.Get("repo") + "/" + q.Get("collection") + "/" + q.Get("rkey"),
			"cid":   rec.CID,
			"value": rec.Value,
		})
	case errors.Is(err, cob.ErrNotFound):
		writeErr(w, http.StatusNotFound, "RecordNotFound", "record not found")
	default:
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
	}
}

func (k *Knot) handleListRecordVersions(w http.ResponseWriter, r *http.Request) {
	// uri = at://<repo>/<collection>/<rkey>
	uri := r.URL.Query().Get("uri")
	parts := strings.SplitN(strings.TrimPrefix(uri, "at://"), "/", 3)
	if len(parts) != 3 {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "bad uri")
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := json.Number(l).Int64(); err == nil && n > 0 && n <= 500 {
			limit = int(n)
		}
	}
	vs, err := k.store.ListVersions(parts[0], parts[1], parts[2], limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "RecordNotFound", "no versions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}
