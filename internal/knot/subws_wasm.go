//go:build js && wasm

package knot

import (
	"net/http"
	"strconv"
	"time"

	"github.com/syumai/workers-go/exp/cloudflare/websocket"

	"github.com/yieldenjoyoors/forgesi/internal/atstream"
	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
)

// handleSubscribeRepos streams the event log live: replay from the cursor,
// then tail. Frames are the same #commit messages getEvents serves. A 25s
// #info keepalive stops intermediaries from idling the socket out.
func (k *Knot) handleSubscribeRepos(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Upgrade(w, r)
	if err != nil {
		return // Upgrade already wrote the failure
	}
	cursor := int64(0)
	if c := r.URL.Query().Get("cursor"); c != "" {
		if n, perr := strconv.ParseInt(c, 10, 64); perr == nil {
			cursor = n
		}
	}
	go func() {
		defer conn.Close(1000, "bye")
		last := cursor
		lastWrite := time.Time{}
		for {
			events := k.eventsAfter(last, 500)
			for _, e := range events {
				frame, ferr := e.frame()
				if ferr != nil {
					continue
				}
				if werr := conn.WriteMessage(websocket.BinaryMessage, frame); werr != nil {
					return
				}
				last = e.Seq
				lastWrite = time.Now()
			}
			if time.Since(lastWrite) > 25*time.Second {
				info, ierr := atstream.Frame{Op: -1, Type: "#info",
					Body: map[string]any{"name": "forgesi-knot"}}.Encode()
				if ierr == nil {
					if werr := conn.WriteMessage(websocket.BinaryMessage, info); werr != nil {
						return
					}
				}
				lastWrite = time.Now()
			}
			time.Sleep(1 * time.Second)
		}
	}()
}

// eventsAfter reads log rows past seq.
func (k *Knot) eventsAfter(seq int64, limit int) []replayEvent {
	if k.opt.DB == nil {
		return nil
	}
	rows, err := k.opt.DB.Query(
		`SELECT seq, time, repo, kind, collection, rkey, rev, prev_rev, cid, change, record
		 FROM knot_events WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []replayEvent
	for rows.Next() {
		var e replayEvent
		if err := rows.Scan(&e.Seq, &e.Time, &e.Repo, &e.Kind, &e.Collection, &e.Rkey,
			&e.Rev, &e.PrevRev, &e.CID, &e.Change, &e.Record); err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

// replayEvent is one log row plus its frame builder.
type replayEvent struct {
	Seq              int64
	Time, Repo, Kind string
	Collection, Rkey string
	Rev, PrevRev     string
	CID, Change      string
	Record           []byte
}

func (e replayEvent) frame() ([]byte, error) {
	if len(e.Record) == 0 {
		// Tombstones and pushes carry no record body: emit as #sync.
		return atstream.Frame{Op: 3, Type: "#sync", Body: map[string]any{
			"did": e.Repo, "seq": e.Seq, "time": e.Time,
		}}.Encode()
	}
	rec, _, err := dagcbor.Decode(e.Record)
	if err != nil {
		return nil, err
	}
	m, ok := rec.(map[string]any)
	if !ok {
		return nil, dagcbor.ErrNotAMap
	}
	return atstream.BuildCommit(atstream.CommitEvent{
		Repo: e.Repo, Rev: e.Rev, Prev: e.PrevRev, Seq: e.Seq, Time: e.Time,
	}, m)
}
