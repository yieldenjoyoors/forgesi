// Package artifactstore is the cob.Backend that keeps git objects and
// refs in a Cloudflare Artifacts repo — the storage authority. The
// knot serves reads from an in-isolate cache (fed by every write and
// by fetch-on-miss); Artifacts holds the durable truth. D1 stays out
// of the content path entirely (metadata only: registry, events,
// runs).
package artifactstore

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/artifacts"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
	"github.com/yieldenjoyoors/forgesi/internal/packfile"
)

const (
	tokenTTL   = 1 * time.Hour
	refCacheTT = 5 * time.Second
)

// Backend is one knot repo's storage, backed by one Artifacts repo.
type Backend struct {
	client  *artifacts.Client
	repo    string
	http    sync.Mutex
	mu      sync.Mutex
	objects map[string][]byte // framed, by oid
	refs    map[string]string
	refAt   map[string]time.Time

	tokMu    sync.Mutex
	readTok  string
	readExp  time.Time
	writeTok string
	writeExp time.Time
}

// New builds the backend for one Artifacts repo.
func New(client *artifacts.Client, repo string) *Backend {
	return &Backend{
		client:  client,
		repo:    repo,
		objects: map[string][]byte{},
		refs:    map[string]string{},
		refAt:   map[string]time.Time{},
	}
}

func (b *Backend) token(scope string) (string, error) {
	b.tokMu.Lock()
	defer b.tokMu.Unlock()
	now := time.Now()
	if scope == "write" && b.writeTok != "" && now.Before(b.writeExp) {
		return b.writeTok, nil
	}
	if scope == "read" && b.readTok != "" && now.Before(b.readExp) {
		return b.readTok, nil
	}
	tok, err := b.client.MintToken(context.Background(), b.repo, scope, uint64(tokenTTL.Seconds()))
	if err != nil {
		return "", err
	}
	// mint round-trips cost more than the ttl is worth hedging: keep a
	// margin so a call at the wire never carries an expired token.
	if scope == "write" {
		b.writeTok, b.writeExp = tok, now.Add(tokenTTL-2*time.Minute)
	} else {
		b.readTok, b.readExp = tok, now.Add(tokenTTL-2*time.Minute)
	}
	return tok, nil
}

// PutObject implements cob.Backend: framed content into the cache;
// durability comes with the next SetRef push that reaches it.
func (b *Backend) PutObject(content []byte) (string, error) {
	oid, framed := frame(content)
	b.mu.Lock()
	b.objects[oid] = framed
	b.mu.Unlock()
	return oid, nil
}

// frame keys a framed object by its git oid — sha1 of the FRAMED bytes
// ("type len\0content"), which is exactly the hash preimage git uses.
func frame(framed []byte) (string, []byte) {
	return sha1Hex(framed), framed
}

// GetObject implements cob.Backend: cache first, fetch-on-miss from
// Artifacts (all heads — small repos, warm isolate).
func (b *Backend) GetObject(oid string) ([]byte, error) {
	b.mu.Lock()
	framed, ok := b.objects[oid]
	b.mu.Unlock()
	if ok {
		return framed, nil
	}
	if err := b.warm(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	framed, ok = b.objects[oid]
	b.mu.Unlock()
	if !ok {
		return nil, cob.ErrNotFound
	}
	return framed, nil
}

// warm fetches every head into the cache.
func (b *Backend) warm() error {
	b.http.Lock()
	defer b.http.Unlock()
	refs, err := b.lsRemote()
	if err != nil {
		return err
	}
	var wants []string
	seen := map[string]bool{}
	for _, r := range refs {
		if !seen[r.OID] {
			seen[r.OID] = true
			wants = append(wants, r.OID)
		}
	}
	if len(wants) == 0 {

		return nil
	}
	tok, err := b.token("read")
	if err != nil {
		return err
	}
	objs, err := b.client.Fetch(context.Background(), b.repo, tok, wants)
	if err != nil {
		return fmt.Errorf("artifactstore warm: %w", err)
	}
	b.mu.Lock()
	for oid, framed := range objs {
		b.objects[oid] = framed
	}
	b.mu.Unlock()
	return nil
}

func (b *Backend) lsRemote() ([]artifacts.Ref, error) {
	tok, err := b.token("read")
	if err != nil {
		return nil, err
	}
	return b.client.LSRemote(context.Background(), b.repo, tok)
}

// RefHead implements cob.Backend.
func (b *Backend) RefHead(ref string) (string, bool, error) {
	b.mu.Lock()
	if oid, ok := b.refs[ref]; ok && time.Since(b.refAt[ref]) < refCacheTT {
		b.mu.Unlock()
		return oid, true, nil
	}
	b.mu.Unlock()
	refs, err := b.lsRemote()
	if err != nil {
		return "", false, err
	}
	b.mu.Lock()
	for _, r := range refs {
		b.refs[r.Name] = r.OID
		b.refAt[r.Name] = time.Now()
	}
	oid, ok := b.refs[ref]
	b.mu.Unlock()
	return oid, ok, nil
}

// SetRef implements cob.Backend: push the ref update with every object
// reachable from the new tip that the cache holds (the remote accepts
// duplicates and dedups).
func (b *Backend) SetRef(ref string, oid string) error {
	b.http.Lock()
	defer b.http.Unlock()
	old := ""
	if refs, err := b.lsRemote(); err == nil {
		for _, r := range refs {
			if r.Name == ref {
				old = r.OID
			}
		}
	}
	// reachability closure from the new tip over the cache; if the walk
	// hits a missing object, warm and retry once.
	objs, err := b.reachable(oid)
	if err != nil {
		if werr := b.warm(); werr != nil {
			return werr
		}
		objs, err = b.reachable(oid)
		if err != nil {
			return fmt.Errorf("artifactstore setRef %s: %w", ref, err)
		}
	}
	pack, err := packfile.Encode(objs)
	if err != nil {
		return err
	}
	if old == "" {
		old = "0000000000000000000000000000000000000000"
	}
	tok, err := b.token("write")
	if err != nil {
		return err
	}
	if err := b.client.Push(context.Background(), b.repo, tok, old, oid, ref, pack); err != nil {
		return fmt.Errorf("artifactstore setRef %s: %w", ref, err)
	}
	b.mu.Lock()
	b.refs[ref] = oid
	b.refAt[ref] = time.Now()
	b.mu.Unlock()
	return nil
}

// ListRefs implements cob.Backend.
func (b *Backend) ListRefs(prefix, after string, limit int) ([]string, error) {
	refs, err := b.lsRemote()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range refs {
		if strings.HasPrefix(r.Name, prefix) && r.Name > after {
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

var _ cob.Backend = (*Backend)(nil)

// ---------- object parsing helpers (framed git objects) ----------

func sha1Hex(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

// splitFrame returns the git type and the RAW content of a framed object.
func splitFrame(framed []byte) (string, []byte) {
	nul := strings.IndexByte(string(framed), 0)
	if nul < 0 {
		return "", nil
	}
	header := string(framed[:nul])
	typ, _, ok := strings.Cut(header, " ")
	if !ok {
		return "", nil
	}
	return typ, framed[nul+1:]
}

func commitField(raw []byte, field string) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, field+" ") {
			return strings.TrimSpace(strings.TrimPrefix(line, field+" "))
		}
		if line == "" {
			break
		}
	}
	return ""
}

// reachable walks the closure from tip over cached objects (commits ->
// parents + tree, trees -> subtrees + blobs) and returns pack objects.
func (b *Backend) reachable(tip string) ([]packfile.Object, error) {
	seen := map[string]bool{}
	var out []packfile.Object
	var visit func(oid string) error
	visit = func(oid string) error {
		if seen[oid] {
			return nil
		}
		b.mu.Lock()
		framed := b.objects[oid]
		b.mu.Unlock()
		if framed == nil {
			return fmt.Errorf("missing object %s in closure", oid)
		}
		typ, raw := splitFrame(framed)
		if typ == "" {
			return fmt.Errorf("bad frame at %s", oid)
		}
		seen[oid] = true
		out = append(out, packfile.Object{Type: packfile.TypeOf(typ), Data: raw, OID: oid})
		switch typ {
		case "commit":
			if t := commitField(raw, "tree"); t != "" {
				if err := visit(t); err != nil {
					return err
				}
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if line == "" {
					break
				}
				if p, ok := strings.CutPrefix(line, "parent "); ok {
					if err := visit(strings.TrimSpace(p)); err != nil {
						return err
					}
				}
			}
		case "tree":
			for _, e := range gitrepo.ParseTree(raw) {
				if err := visit(e.OID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(tip); err != nil {
		return nil, err
	}
	return out, nil
}
