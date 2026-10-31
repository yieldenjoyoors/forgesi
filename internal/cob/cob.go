// Package cob is the COB record store: Tangled collaborative objects —
// issues, pull requests, comments, labels — persisted as version chains of
// real git objects under refs/cobs/<collection>/<rkey>. Every write produces
// a git commit whose SHA is the change id (x-tngl-change); record bodies are
// dag-cbor blobs whose CIDv1 is the record cid.
package cob

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
	"github.com/yieldenjoyoors/forgesi/internal/tid"
)

var (
	// ErrInvalidSwap is the atproto 409: the swap guard cid does not match
	// the current record version (concurrent write lost the race).
	ErrInvalidSwap = errors.New("InvalidSwap")
	// ErrNotFound maps to atproto RecordNotFound.
	ErrNotFound = errors.New("RecordNotFound")
	// ErrBadRequest maps to InvalidRequest.
	ErrBadRequest = errors.New("InvalidRequest")
)

// Backend persists git objects and refs. MemoryBackend for tests and local
// dev; the Artifacts/git backend (M2b) stores the same bytes.
type Backend interface {
	PutObject(content []byte) (oid string, err error) // raw object; oid = sha1 hex
	GetObject(oid string) ([]byte, error)
	RefHead(ref string) (oid string, ok bool, err error)
	SetRef(ref string, oid string) error
	// ListRefs enumerates refs under a prefix, ordered lexically, after the
	// cursor, up to limit.
	ListRefs(prefix, after string, limit int) ([]string, error)
}

// MemoryBackend is an in-memory Backend.
type MemoryBackend struct {
	objects map[string][]byte
	refs    map[string]string
}

// MemoryBackend constructs an empty in-memory backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{objects: map[string][]byte{}, refs: map[string]string{}}
}

func (m *MemoryBackend) PutObject(content []byte) (string, error) {
	oid := objectID(content)
	m.objects[oid] = content
	return oid, nil
}

func (m *MemoryBackend) GetObject(oid string) ([]byte, error) {
	c, ok := m.objects[oid]
	if !ok {
		return nil, fmt.Errorf("object %s not found", oid)
	}
	return c, nil
}

func (m *MemoryBackend) RefHead(ref string) (string, bool, error) {
	oid, ok := m.refs[ref]
	return oid, ok, nil
}

func (m *MemoryBackend) SetRef(ref string, oid string) error {
	m.refs[ref] = oid
	return nil
}

// ListRefs implements Backend.ListRefs from the in-memory map.
func (m *MemoryBackend) ListRefs(prefix, after string, limit int) ([]string, error) {
	var keys []string
	for k := range m.refs {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

// objectID computes a git object id: sha1 over "<kind> <len>\0<content>".
// Callers pass fully-formed objects (header included in content).
func objectID(content []byte) string {
	sum := sha1.Sum(content)
	return hex.EncodeToString(sum[:])
}

// Store serves record operations for every repo account on the knot.
type Store struct {
	be      Backend
	knotDID string
}

// New builds a record store.
func New(be Backend, knotDID string) (*Store, error) {
	return &Store{be: be, knotDID: knotDID}, nil
}

// Write is the result of a record write: the at-uri parts plus the knot
// change id (git commit SHA) and the editor the knot stamps into the record.
type Write struct {
	Repo, Collection, Rkey string
	CID, Change, Editor    string
}

// URI renders the at:// record uri.
func (w Write) URI() string { return "at://" + w.Repo + "/" + w.Collection + "/" + w.Rkey }

// Version is one entry of the version history.
type Version struct {
	CID       string `json:"cid"`
	ChangeID  string `json:"changeId"`
	Editor    string `json:"-"`
	CreatedAt string `json:"-"`
}

// Record is a materialized record.
type Record struct {
	CID, Change, Editor string
	Value               map[string]any
}

// commitBody is the JSON message carried by each COB version commit.
type commitBody struct {
	Blob      string `json:"blob"`
	CID       string `json:"cid"`
	Editor    string `json:"editor"`
	CreatedAt string `json:"createdAt"`
	Deleted   bool   `json:"deleted,omitempty"`
}

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Create writes a new record; an empty rkey mints a TID.
func (s *Store) Create(repo, collection, rkey string, record map[string]any, editor string) (Write, error) {
	if record == nil {
		return Write{}, fmt.Errorf("%w: empty record", ErrBadRequest)
	}
	if rkey == "" {
		rkey = tid.Now().String()
	} else if _, err := tid.Parse(rkey); err != nil {
		// Custom rkeys are allowed (labels, 'self' manifests); TIDs pass above.
		if !validRkey(rkey) {
			return Write{}, fmt.Errorf("%w: bad rkey %q", ErrBadRequest, rkey)
		}
	}
	ref := cobRef(repo, collection, rkey)
	if _, ok, _ := s.be.RefHead(ref); ok {
		return Write{}, fmt.Errorf("%w: record already exists", ErrInvalidSwap)
	}
	if record["$type"] == nil {
		record["$type"] = collection
	}
	w, err := s.append(ref, "", record, editor, false)
	if err != nil {
		return Write{}, err
	}
	w.Repo, w.Collection, w.Rkey = repo, collection, rkey
	return w, nil
}

// Put replaces a record version, guarded by the optional swap cid.
func (s *Store) Put(repo, collection, rkey string, record map[string]any, swapCID, editor string) (Write, error) {
	ref := cobRef(repo, collection, rkey)
	head, ok, err := s.be.RefHead(ref)
	if err != nil {
		return Write{}, err
	}
	if !ok {
		return Write{}, fmt.Errorf("%w: no such record", ErrNotFound)
	}
	cur, err := s.readVersion(head)
	if err != nil {
		return Write{}, err
	}
	if cur.Deleted {
		return Write{}, fmt.Errorf("%w: no such record", ErrNotFound)
	}
	if swapCID != "" && swapCID != cur.CID {
		return Write{}, ErrInvalidSwap
	}
	if record["$type"] == nil {
		record["$type"] = collection
	}
	w, err := s.append(ref, head, record, editor, false)
	if err != nil {
		return Write{}, err
	}
	w.Repo, w.Collection, w.Rkey = repo, collection, rkey
	return w, nil
}

// Delete tombstones a record; the chain keeps history for listVersions.
func (s *Store) Delete(repo, collection, rkey, swapCID, editor string) (Write, error) {
	ref := cobRef(repo, collection, rkey)
	head, ok, err := s.be.RefHead(ref)
	if err != nil {
		return Write{}, err
	}
	if !ok {
		return Write{}, fmt.Errorf("%w: no such record", ErrNotFound)
	}
	cur, err := s.readVersion(head)
	if err != nil {
		return Write{}, err
	}
	// A stale swap guard loses the race check before existence: the caller
	// believed a version was head that no longer is.
	if swapCID != "" && swapCID != cur.CID {
		return Write{}, ErrInvalidSwap
	}
	if cur.Deleted {
		return Write{}, fmt.Errorf("%w: no such record", ErrNotFound)
	}
	w, err := s.append(ref, head, nil, editor, true)
	if err != nil {
		return Write{}, err
	}
	w.Repo, w.Collection, w.Rkey = repo, collection, rkey
	return w, nil
}

// Get materializes the current record version.
func (s *Store) Get(repo, collection, rkey string) (Record, error) {
	ref := cobRef(repo, collection, rkey)
	head, ok, err := s.be.RefHead(ref)
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, ErrNotFound
	}
	v, err := s.readVersion(head)
	if err != nil {
		return Record{}, err
	}
	if v.Deleted {
		return Record{}, ErrNotFound
	}
	return Record{CID: v.CID, Change: head, Editor: v.Editor, Value: v.Value}, nil
}

// ListVersions walks the chain oldest-first.
func (s *Store) ListVersions(repo, collection, rkey string, limit int, cursor string) ([]Version, error) {
	ref := cobRef(repo, collection, rkey)
	head, ok, err := s.be.RefHead(ref)
	if err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	var chain []string
	oid := head
	for oid != "" {
		chain = append(chain, oid)
		v, err := s.readVersion(oid)
		if err != nil {
			return nil, err
		}
		oid = v.Parent
	}
	// oldest-first; cursor is an exclusive change id (pagination walks back).
	start := 0
	if cursor != "" {
		for i, c := range chain {
			if c == cursor {
				start = i + 1
				break
			}
		}
	}
	var out []Version
	for i := len(chain) - 1 - start; i >= 0 && len(out) < limit; i-- {
		v, err := s.readVersion(chain[i])
		if err != nil {
			return nil, err
		}
		out = append(out, Version{CID: v.CID, ChangeID: chain[i], Editor: v.Editor, CreatedAt: v.CreatedAt})
	}
	return out, nil
}

// version is a decoded chain link.
type version struct {
	Parent    string
	Blob      string
	CID       string
	Editor    string
	CreatedAt string
	Deleted   bool
	Value     map[string]any
}

func (s *Store) readVersion(oid string) (version, error) {
	content, err := s.be.GetObject(oid)
	if err != nil {
		return version{}, err
	}
	// git commit: headers, blank line, JSON body
	msg := commitMessage(content)
	var body commitBody
	if err := json.Unmarshal([]byte(msg), &body); err != nil {
		return version{}, fmt.Errorf("bad cob commit %s: %w", oid, err)
	}
	v := version{Parent: commitParent(content), CID: body.CID, Editor: body.Editor,
		CreatedAt: body.CreatedAt, Deleted: body.Deleted}
	if !v.Deleted {
		blob, err := s.be.GetObject(body.Blob)
		if err != nil {
			return version{}, err
		}
		// Objects carry their git header ("<kind> <len>\0"); the record is
		// the content after the NUL.
		val, err := decodeRecord(objectContent(blob))
		if err != nil {
			return version{}, err
		}
		v.Value = val
	}
	return v, nil
}

// append stores blob+commit and advances the ref. Returns the write result.
func (s *Store) append(ref, parent string, record map[string]any, editor string, deleted bool) (Write, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cid := ""
	blobOID := ""
	if !deleted {
		bytes, err := dagcbor.Encode(record)
		if err != nil {
			return Write{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		cid, err = dagcbor.CIDString(bytes)
		if err != nil {
			return Write{}, err
		}
		// store as a git blob object
		blobOID, err = s.be.PutObject(gitObject("blob", bytes))
		if err != nil {
			return Write{}, err
		}
	}
	body, _ := json.Marshal(commitBody{Blob: blobOID, CID: cid, Editor: editor, CreatedAt: now, Deleted: deleted})
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	commit := "tree " + emptyTree + "\n"
	if parent != "" {
		commit += "parent " + parent + "\n"
	}
	ident := "forgesi-knot <" + emailSafe(editor) + "@knot> " + ts + " +0000\n"
	commit += "author " + ident + "committer " + ident + "\n" + string(body) + "\n"
	oid, err := s.be.PutObject(gitObject("commit", []byte(commit)))
	if err != nil {
		return Write{}, err
	}
	if err := s.be.SetRef(ref, oid); err != nil {
		return Write{}, err
	}
	return Write{CID: cid, Change: oid, Editor: editor}, nil
}

func cobRef(repo, collection, rkey string) string {
	return "refs/cobs/" + repo + "/" + collection + "/" + rkey
}

func gitObject(kind string, content []byte) []byte {
	header := kind + " " + strconv.Itoa(len(content)) + "\x00"
	return append([]byte(header), content...)
}

// objectContent strips the git object header ("<kind> <len>\0").
func objectContent(obj []byte) []byte {
	for i := 0; i < len(obj); i++ {
		if obj[i] == 0 {
			return obj[i+1:]
		}
	}
	return obj
}

// commitMessage extracts the message (after the first blank line) of a git
// commit object.
func commitMessage(content []byte) string {
	s := string(content)
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\n' && s[i+1] == '\n' {
			return s[i+2:]
		}
	}
	return ""
}

func commitParent(content []byte) string {
	s := string(content)
	for _, line := range splitLines(s) {
		if len(line) > 7 && line[:7] == "parent " {
			return line[7:]
		}
	}
	return ""
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func emailSafe(did string) string {
	out := make([]byte, 0, len(did))
	for i := 0; i < len(did); i++ {
		c := did[i]
		if c == ':' || c == '/' {
			c = '.'
		}
		out = append(out, c)
	}
	return string(out)
}

// validRkey follows atproto rkey rules: 1-512 chars of [a-zA-Z0-9_:-].
func validRkey(r string) bool {
	if len(r) < 1 || len(r) > 512 {
		return false
	}
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

// decodeRecord converts a dag-cbor blob back to a JSON-shaped map. M2
// stores carry a parallel JSON copy in the commit body? No: decode via
// cbor→json bridge implemented here minimally for our value shapes.
func decodeRecord(blob []byte) (map[string]any, error) {
	v, rest, err := decodeCBOR(blob)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("trailing bytes in record blob")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("record blob is not a map")
	}
	return m, nil
}

// ListRefs returns refs under a prefix (e.g. "refs/cobs/<repo>/<collection>/"),
// ordered lexically, starting strictly after `after` (pagination cursor).
// The record-listing surface (com.atproto.repo.listRecords) walks it.
func (s *Store) ListRefs(prefix, after string, limit int) ([]string, error) {
	return s.be.ListRefs(prefix, after, limit)
}

// ListRecords materializes every live record in a collection. Live only —
// tombstoned chains are skipped (atproto never lists deleted records).
func (s *Store) ListRecords(repo, collection string, limit int, cursor string) ([]Record, []string, error) {
	prefix := cobRef(repo, collection, "")
	refs, err := s.be.ListRefs(prefix, cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	var out []Record
	var rkeys []string
	for _, ref := range refs {
		rkey := ref[len(prefix):]
		head, ok, err := s.be.RefHead(ref)
		if err != nil || !ok {
			continue
		}
		v, err := s.readVersion(head)
		if err != nil || v.Deleted {
			continue
		}
		out = append(out, Record{CID: v.CID, Change: head, Editor: v.Editor, Value: v.Value})
		rkeys = append(rkeys, rkey)
	}
	return out, rkeys, nil
}
