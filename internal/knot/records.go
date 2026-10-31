package knot

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

// RepoStore is the repo registry (D1-backed in production).
type RepoStore interface {
	create(did, name, branch string) (ok bool)
	byDID(did string) (*repoAccount, bool)
	list() []repoAccount
}

// repoAccount is one repo hosted by the knot.
type repoAccount struct {
	Did, Name, DefaultBranch string
}

// memoryRepoStore is the in-memory registry (unit tests).
type memoryRepoStore struct {
	mu    sync.Mutex
	repos map[string]*repoAccount
}

func newMemoryRepoStore() *memoryRepoStore {
	return &memoryRepoStore{repos: map[string]*repoAccount{}}
}

func (m *memoryRepoStore) create(did, name, branch string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repos[name]; ok {
		return false
	}
	m.repos[name] = &repoAccount{Did: did, Name: name, DefaultBranch: branch}
	return true
}

func (m *memoryRepoStore) byDID(did string) (*repoAccount, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repos {
		if r.Did == did {
			return r, true
		}
	}
	return nil, false
}

func (m *memoryRepoStore) list() []repoAccount {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]repoAccount, 0, len(m.repos))
	for _, r := range m.repos {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// d1RepoStore persists the registry; isolates share nothing, D1 does.
type d1RepoStore struct{ db *sql.DB }

// NewD1RepoStore builds the SQL-backed registry.
func NewD1RepoStore(db *sql.DB) RepoStore { return &d1RepoStore{db: db} }

func (d *d1RepoStore) create(did, name, branch string) bool {
	res, err := d.db.Exec(
		`INSERT OR IGNORE INTO knot_repos (did, name, default_branch, created_at) VALUES (?, ?, ?, ?)`,
		did, name, branch, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func (d *d1RepoStore) byDID(did string) (*repoAccount, bool) {
	row := d.db.QueryRow(`SELECT did, name, default_branch FROM knot_repos WHERE did = ?`, did)
	var r repoAccount
	if err := row.Scan(&r.Did, &r.Name, &r.DefaultBranch); err != nil {
		return nil, false
	}
	return &r, true
}

func (d *d1RepoStore) list() []repoAccount {
	rows, err := d.db.Query(`SELECT did, name, default_branch FROM knot_repos ORDER BY name`)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []repoAccount
	for rows.Next() {
		var r repoAccount
		if err := rows.Scan(&r.Did, &r.Name, &r.DefaultBranch); err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
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
	k.mux.HandleFunc("GET /xrpc/org.tangled.repo.listRepos", k.handleListRepos)
	k.mux.HandleFunc("GET /xrpc/com.atproto.repo.listRecords", k.handleListRecords)
}

// handleListRepos serves the knot's repo listing (our client's index).
func (k *Knot) handleListRepos(w http.ResponseWriter, r *http.Request) {
	type repoView struct {
		Did           string `json:"did"`
		Name          string `json:"name"`
		DefaultBranch string `json:"defaultBranch"`
	}
	_ = repoView{}
	repos := k.registry.list()
	out := make([]repoView, 0, len(repos))
	for _, rp := range repos {
		v := repoView(rp)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

// handleListRecords follows the atproto listRecords shape.
func (k *Knot) handleListRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo := q.Get("repo")
	collection := q.Get("collection")
	if _, ok := k.registry.byDID(repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	limit := 50
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	cursor := q.Get("cursor")
	prefix := "refs/cobs/" + repo + "/" + collection + "/"
	if cursor != "" {
		cursor = prefix + cursor
	}
	recs, rkeys, err := k.store.ListRecords(repo, collection, limit, cursor)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	type recordView struct {
		URI   string         `json:"uri"`
		CID   string         `json:"cid"`
		Value map[string]any `json:"value"`
	}
	out := make([]recordView, 0, len(recs))
	for i, rec := range recs {
		out = append(out, recordView{
			URI:   "at://" + repo + "/" + collection + "/" + rkeys[i],
			CID:   rec.CID,
			Value: rec.Value,
		})
	}
	resp := map[string]any{"records": out}
	if len(rkeys) == limit {
		resp["cursor"] = rkeys[len(rkeys)-1]
	}
	writeJSON(w, http.StatusOK, resp)
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
