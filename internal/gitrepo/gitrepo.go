// Package gitrepo implements the git object model (trees, commits, refs)
// over the knot's object store: framed objects and refs in D1 today,
// Artifacts later — same Backend shape.
package gitrepo

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/packfile"
)

// Backend persists framed objects and refs (cob.Backend satisfies this).
type Backend interface {
	PutObject(content []byte) (oid string, err error)
	GetObject(oid string) ([]byte, error)
	RefHead(ref string) (oid string, ok bool, err error)
	SetRef(ref string, oid string) error
	ListRefs(prefix, after string, limit int) ([]string, error)
}

// Repo is one git repository's object/ref namespace.
type Repo struct {
	Be Backend
}

// Parsed is a decoded framed object.
type Parsed struct {
	Type string
	Data []byte
}

// Parse splits a framed object into type and content.
func Parse(framed []byte) (Parsed, error) {
	i := bytes.IndexByte(framed, 0)
	if i < 0 {
		return Parsed{}, fmt.Errorf("gitrepo: object has no header")
	}
	header := string(framed[:i])
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return Parsed{}, fmt.Errorf("gitrepo: bad object header %q", header)
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n != len(framed)-i-1 {
		return Parsed{}, fmt.Errorf("gitrepo: header size mismatch")
	}
	return Parsed{Type: parts[0], Data: framed[i+1:]}, nil
}

// StoreObject stores a full (already-composed) object, framed.
func (r Repo) StoreObject(typ string, content []byte) (string, error) {
	return r.Be.PutObject(packfile.GitObject(typ, content))
}

// ReadObject parses the framed object at oid.
func (r Repo) ReadObject(oid string) (Parsed, error) {
	framed, err := r.Be.GetObject(oid)
	if err != nil {
		return Parsed{}, err
	}
	return Parse(framed)
}

// TreeEntry is one tree line.
type TreeEntry struct {
	Mode string // "100644", "100755", "40000", "120000"
	Name string
	OID  string
}

// EncodeTree composes tree content; entries must be git-sorted (the git
// rule: plain name order, but directories sort as if suffixed with "/").
func EncodeTree(entries []TreeEntry) []byte {
	sort.SliceStable(entries, func(i, j int) bool {
		return treeSortKey(entries[i]) < treeSortKey(entries[j])
	})
	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString(e.Mode)
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
		buf.WriteByte(0)
		buf.Write(hexToBytes(e.OID))
	}
	return buf.Bytes()
}

func treeSortKey(e TreeEntry) string {
	if e.Mode == "40000" {
		return e.Name + "/"
	}
	return e.Name
}

func hexToBytes(oid string) []byte {
	out := make([]byte, len(oid)/2)
	for i := 0; i+1 < len(oid); i += 2 {
		v := hexVal(oid[i])<<4 | hexVal(oid[i+1])
		out[i/2] = byte(v)
	}
	return out
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// CommitSpec builds commit content.
type CommitSpec struct {
	Tree    string
	Parents []string
	Message string
	When    time.Time
	Ident   string // "Name <email>"
}

// EncodeCommit composes commit content.
func EncodeCommit(spec CommitSpec) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "tree %s\n", spec.Tree)
	for _, p := range spec.Parents {
		fmt.Fprintf(&buf, "parent %s\n", p)
	}
	ts := strconv.FormatInt(spec.When.Unix(), 10)
	fmt.Fprintf(&buf, "author %s %s +0000\n", spec.Ident, ts)
	fmt.Fprintf(&buf, "committer %s %s +0000\n", spec.Ident, ts)
	buf.WriteString("\n")
	buf.WriteString(spec.Message)
	if !strings.HasSuffix(spec.Message, "\n") {
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

// WalkReachable collects every object reachable from the given roots
// (commit oids), breadth-first, deduped — the fetch-side object set.
func (r Repo) WalkReachable(roots []string) ([]packfile.Object, error) {
	seen := map[string]bool{}
	var out []packfile.Object
	queue := append([]string{}, roots...)
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if oid == "" || seen[oid] {
			continue
		}
		p, err := r.ReadObject(oid)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", oid, err)
		}
		seen[oid] = true
		out = append(out, packfile.Object{Type: packfile.TypeOf(p.Type), Data: p.Data, OID: oid})
		switch p.Type {
		case "commit":
			for _, line := range strings.Split(string(p.Data), "\n") {
				if strings.HasPrefix(line, "tree ") || strings.HasPrefix(line, "parent ") {
					queue = append(queue, strings.TrimSpace(strings.SplitN(line, " ", 2)[1]))
				}
			}
		case "tree":
			for _, e := range ParseTree(p.Data) {
				if e.Mode != "160000" { // gitlinks point outside
					queue = append(queue, e.OID)
				}
			}
		}
	}
	return out, nil
}

// ParseTree decodes tree content into entries.
func ParseTree(data []byte) []TreeEntry {
	var out []TreeEntry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		nul := bytes.IndexByte(data, 0)
		if sp < 0 || nul < sp {
			return out
		}
		mode := string(data[:sp])
		name := string(data[sp+1 : nul])
		if nul+21 > len(data) {
			return out
		}
		oid := fmt.Sprintf("%x", data[nul+1:nul+21])
		out = append(out, TreeEntry{Mode: mode, Name: name, OID: oid})
		data = data[nul+21:]
	}
	return out
}

// ApplyPush stores decoded objects and advances refs (each new oid must be
// present in the pack or already stored). Returns applied ref updates.
func (r Repo) ApplyPush(objects []packfile.Object, updates map[string]string) error {
	stored := map[string]bool{}
	for _, o := range objects {
		if _, err := r.Be.PutObject(packfile.GitObject(packfile.TypeName(o.Type), o.Data)); err != nil {
			return err
		}
		stored[o.OID] = true
	}
	for ref, newOID := range updates {
		if newOID != "" { // "" deletes the ref
			if !stored[newOID] {
				if _, err := r.ReadObject(newOID); err != nil {
					return fmt.Errorf("push %s: object %s missing", ref, newOID)
				}
			}
		}
		if err := r.Be.SetRef("refs/heads/"+strings.TrimPrefix(ref, "refs/heads/"), newOID); err != nil {
			return err
		}
	}
	return nil
}

// Branches lists refs/heads/* with their heads.
func (r Repo) Branches() (map[string]string, error) {
	refs, err := r.Be.ListRefs("refs/heads/", "", 1000)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ref := range refs {
		if oid, ok, err := r.Be.RefHead(ref); err == nil && ok && oid != "" {
			out[strings.TrimPrefix(ref, "refs/heads/")] = oid
		}
	}
	return out, nil
}
