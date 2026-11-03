package knot

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/atstream"
	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
	"github.com/yieldenjoyoors/forgesi/internal/tid"
)

// The firehose's durable side: every record write and push appends to
// knot_events; consumers replay it (cursor = seq). The WebSocket
// subscribeRepos endpoint streams the same frames live.

type emitter struct{ db *sql.DB }

// append records one event; rev is a fresh TID, prev is the repo's last.
func (e emitter) append(repo, kind, collection, rkey, cid, change string, record map[string]any) error {
	if e.db == nil {
		return nil // in-memory knot (unit tests): no log
	}
	var prev string
	_ = e.db.QueryRow(`SELECT rev FROM knot_events WHERE repo = ? ORDER BY seq DESC LIMIT 1`, repo).Scan(&prev)
	var recBytes []byte
	if record != nil {
		b, err := dagcbor.Encode(record)
		if err != nil {
			return err
		}
		recBytes = b
	}
	_, err := e.db.Exec(
		`INSERT INTO knot_events (time, repo, kind, collection, rkey, rev, prev_rev, cid, change, record) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), repo, kind, collection, rkey,
		tid.Now().String(), prev, cid, change, recBytes)
	return err
}

// eventRow is one replayable log entry.
type eventRow struct {
	Seq        int64  `json:"seq"`
	Time       string `json:"time"`
	Repo       string `json:"repo"`
	Kind       string `json:"kind"`
	Collection string `json:"collection"`
	Rkey       string `json:"rkey"`
	Rev        string `json:"rev"`
	PrevRev    string `json:"prevRev"`
	CID        string `json:"cid"`
	Change     string `json:"change"`
	Record     []byte `json:"-"`
}

func (k *Knot) firehoseRoutes() {
	k.mux.HandleFunc("GET /xrpc/com.atproto.sync.getEvents", k.handleGetEvents)
	k.mux.HandleFunc("GET /xrpc/com.atproto.sync.getLatestCommit", k.handleGetLatestCommit)
	k.mux.HandleFunc("GET /xrpc/com.atproto.sync.getRecord", k.handleSyncGetRecord)
	k.mux.HandleFunc("GET /xrpc/com.atproto.sync.getRepoStatus", k.handleGetRepoStatus)
}

// handleGetEvents replays the firehose over HTTP: frames built on the fly,
// ordered by seq, resumable by cursor. Each entry carries the binary frame
// base64-encoded (consumers feed it to their atproto decoder).
func (k *Knot) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	if k.opt.DB == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}, "cursor": 0})
		return
	}
	cursor := int64(0)
	if c := r.URL.Query().Get("cursor"); c != "" {
		if n, err := strconv.ParseInt(c, 10, 64); err == nil {
			cursor = n
		}
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	rows, err := k.opt.DB.Query(
		`SELECT seq, time, repo, kind, collection, rkey, rev, prev_rev, cid, change, record
		 FROM knot_events WHERE seq > ? ORDER BY seq LIMIT ?`, cursor, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	defer func() { _ = rows.Close() }()
	type outEvent struct {
		Seq        int64  `json:"seq"`
		Repo       string `json:"repo"`
		Kind       string `json:"kind"`
		Rev        string `json:"rev"`
		Prev       string `json:"prev"`
		Change     string `json:"change"`
		Collection string `json:"collection,omitempty"` // set: a record write; empty: a git push
		Rkey       string `json:"rkey,omitempty"`
		CID        string `json:"cid,omitempty"` // the commit oid on git pushes
		Frame      string `json:"frame"`         // base64 atproto event-stream frame
	}
	var out []outEvent
	last := cursor
	for rows.Next() {
		var e eventRow
		if err := rows.Scan(&e.Seq, &e.Time, &e.Repo, &e.Kind, &e.Collection, &e.Rkey,
			&e.Rev, &e.PrevRev, &e.CID, &e.Change, &e.Record); err != nil {
			writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
			return
		}
		oe := outEvent{Seq: e.Seq, Repo: e.Repo, Kind: e.Kind, Rev: e.Rev, Prev: e.PrevRev, Change: e.Change, Collection: e.Collection, Rkey: e.Rkey, CID: e.CID}
		if len(e.Record) > 0 {
			rec, _, err := dagcbor.Decode(e.Record)
			if err == nil {
				if m, ok := rec.(map[string]any); ok {
					frame, err := atstream.BuildCommit(atstream.CommitEvent{
						Repo: e.Repo, Rev: e.Rev, Prev: e.PrevRev, Seq: e.Seq, Time: e.Time,
					}, m)
					if err == nil {
						oe.Frame = base64.StdEncoding.EncodeToString(frame)
					}
				}
			}
		}
		out = append(out, oe)
		last = e.Seq
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "cursor": last})
}

func (k *Knot) handleGetLatestCommit(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("did")
	if repo == "" {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "missing did")
		return
	}
	if _, ok := k.registry.byDID(repo); !ok {
		writeErr(w, http.StatusNotFound, "RepoNotFound", "no such repo")
		return
	}
	var rev, prev string
	if k.opt.DB != nil {
		_ = k.opt.DB.QueryRow(`SELECT rev, prev_rev FROM knot_events WHERE repo = ? ORDER BY seq DESC LIMIT 1`, repo).Scan(&rev, &prev)
	}
	if rev == "" {
		rev = tid.Now().String()
	}
	writeJSON(w, http.StatusOK, map[string]string{"cid": "bafyre" + rev, "rev": rev})
}

// handleSyncGetRecord serves the raw dag-cbor record bytes (the sync flavor
// used by slingshot-style record fetchers).
func (k *Knot) handleSyncGetRecord(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rec, err := k.store.Get(q.Get("did"), q.Get("collection"), q.Get("rkey"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "RecordNotFound", "record not found")
		return
	}
	bytes, err := dagcbor.Encode(rec.Value)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.ipld.dag-cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)
}

func (k *Knot) handleGetRepoStatus(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("did")
	if repo == "" {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "missing did")
		return
	}
	if _, ok := k.registry.byDID(repo); ok {
		writeJSON(w, http.StatusOK, map[string]any{"did": repo, "active": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"did": repo, "active": false})
}

var _ = fmt.Sprintf
var _ = json.Marshal

// emit appends to the firehose log (D1-backed knots only).
func (k *Knot) emit(repo, kind, collection, rkey, cid, change string, record map[string]any) {
	if k.opt.DB == nil {
		return
	}
	_ = emitter{db: k.opt.DB}.append(repo, kind, collection, rkey, cid, change, record)
}
