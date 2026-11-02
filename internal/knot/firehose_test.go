package knot

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/cob"
	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
)

// M3 over HTTP: writes append to the event log; getEvents replays frames
// that decode as atproto #commit messages; repo DID docs resolve.
func TestFirehoseEventsAndDIDDocs(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{Host: "forgesi.org", Version: "0.1.0",
		Auth: DevAuthorizer{DID: "did:plc:writer"}, DB: db})

	post := func(path, body string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer dev")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/xrpc/org.tangled.repo.createRepo", `{"name":"fh"}`)
	post("/xrpc/com.atproto.repo.createRecord",
		`{"repo":"did:web:forgesi.org:repo:fh","collection":"org.tangled.track.ticket","record":{"$type":"org.tangled.track.ticket","title":{"$type":"org.tangled.markup.markdown#inline","text":"streamed"},"state":"open","createdAt":"2026-10-31T12:00:00.000Z"}}`)

	// events replay with decodable frames
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/xrpc/com.atproto.sync.getEvents", nil))
	if w.Code != 200 {
		t.Fatalf("getEvents: %d", w.Code)
	}
	var out struct {
		Events []struct {
			Seq   int64  `json:"seq"`
			Repo  string `json:"repo"`
			Frame string `json:"frame"`
		} `json:"events"`
		Cursor int64 `json:"cursor"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// createRepo emits its own event + the manifest record (2)
	if len(out.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(out.Events))
	}
	if out.Events[0].Repo != "did:web:forgesi.org:repo:fh" {
		t.Errorf("repo = %s", out.Events[0].Repo)
	}
	frame, err := base64.StdEncoding.DecodeString(out.Events[0].Frame)
	if err != nil {
		t.Fatal(err)
	}
	hdrAny, rest, err := dagcbor.Decode(frame)
	if err != nil {
		t.Fatalf("frame header: %v", err)
	}
	hdr := hdrAny.(map[string]any)
	if hdr["t"] != "#commit" {
		t.Fatalf("frame type = %v", hdr["t"])
	}
	bodyAny, _, err := dagcbor.Decode(rest)
	if err != nil {
		t.Fatalf("frame body: %v", err)
	}
	if bodyAny.(map[string]any)["did"] != "did:web:forgesi.org:repo:fh" {
		t.Errorf("frame did = %v", bodyAny)
	}

	// cursor pagination: nothing new after the last seq
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.sync.getEvents?cursor="+itoa(out.Cursor), nil))
	var next struct {
		Events []json.RawMessage `json:"events"`
	}
	_ = json.NewDecoder(w.Body).Decode(&next)
	if len(next.Events) != 0 {
		t.Errorf("replay after cursor returned %d events", len(next.Events))
	}

	// repo DID doc resolves with the TangledKnot service
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/repo:fh/did.json", nil))
	if w.Code != 200 {
		t.Fatalf("repo did doc: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"TangledKnot"`) ||
		!strings.Contains(w.Body.String(), `"AtprotoPersonalDataServer"`) ||
		!strings.Contains(w.Body.String(), "did:web:forgesi.org:repo:fh") {
		t.Errorf("did doc body = %s", w.Body.String())
	}

	// sync.getLatestCommit + sync.getRecord
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.sync.getLatestCommit?did=did:web:forgesi.org:repo:fh", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"rev"`) {
		t.Errorf("getLatestCommit: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.sync.getRecord?did=did:web:forgesi.org:repo:fh&collection=org.tangled.track.ticket&rkey="+lastRkey(t, h), nil))
	if w.Code != 200 {
		t.Fatalf("sync.getRecord: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/vnd.ipld.dag-cbor" {
		t.Errorf("content-type = %s", w.Header().Get("Content-Type"))
	}
}

func lastRkey(t *testing.T, h *Knot) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:fh&collection=org.tangled.track.ticket", nil))
	var out struct {
		Records []struct {
			URI string `json:"uri"`
		} `json:"records"`
	}
	_ = json.NewDecoder(w.Body).Decode(&out)
	if len(out.Records) == 0 {
		t.Fatal("no records")
	}
	uri := out.Records[0].URI
	return uri[strings.LastIndex(uri, "/")+1:]
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
