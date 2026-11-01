package knot

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/cob"

	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
	"github.com/yieldenjoyoors/forgesi/internal/packfile"
	"github.com/yieldenjoyoors/forgesi/internal/packline"
)

// Git smart-HTTP v2 over the knot's object store. Upload-pack (clone/fetch)
// is anonymous like every knot; receive-pack (push) requires a service-auth
// JWT with aud=the knot DID and lxm=org.tangled.git.receivePack.

const (
	gitContentTypeAdvertisement = "application/x-git-upload-pack-advertisement"
	gitContentTypeResult        = "application/x-git-upload-pack-result"
	gitContentTypePackResult    = "application/x-git-receive-pack-result"
	receivePackLxm              = "org.tangled.git.receivePack"
)

func (k *Knot) gitRoutes() {
	k.mux.HandleFunc("GET /{did}/info/refs", k.handleInfoRefs)
	k.mux.HandleFunc("POST /{did}/git-upload-pack", k.handleUploadPack)
	k.mux.HandleFunc("POST /{did}/git-receive-pack", k.handleReceivePack)
}

// repoFor resolves the repo account and its git namespace.
func (k *Knot) repoFor(w http.ResponseWriter, r *http.Request) (*repoAccount, gitrepo.Repo, bool) {
	did := r.PathValue("did")
	did = strings.TrimSuffix(did, ".git")
	acc, ok := k.registry.byDID(did)
	if !ok {
		writeErr(w, http.StatusNotFound, "NotFound", "no such repo")
		return nil, gitrepo.Repo{}, false
	}
	return acc, gitrepo.Repo{Be: k.storeBackend(), RefPrefix: "git/" + acc.Did + "/"}, true
}

func (k *Knot) storeBackend() cob.Backend { return k.be }

func (k *Knot) handleInfoRefs(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := k.repoFor(w, r)
	if !ok {
		return
	}
	service := r.URL.Query().Get("service")
	switch service {
	case "git-upload-pack":
		// v2: the advertisement is the capability list directly.
		if r.Header.Get("Git-Protocol") == "version=2" || strings.Contains(r.Header.Get("Git-Protocol"), "version=2") {
			w.Header().Set("Content-Type", gitContentTypeAdvertisement)
			w.Header().Set("Cache-Control", "no-cache")
			for _, line := range []string{
				"version 2",
				"agent=forgesi-knot/0.1",
				"ls-refs=unborn",
				"fetch=shallow wait-for-done",
				"server-option",
				"object-format=sha1",
			} {
				_, _ = w.Write(packline.Pkt(line + "\n"))
			}
			_, _ = w.Write(packline.FlushPkt())
			return
		}
		// v0 advertisement: refs (possibly empty) + capabilities.
		w.Header().Set("Content-Type", gitContentTypeAdvertisement)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(packline.Pkt("# service=git-upload-pack\n"))
		_, _ = w.Write(packline.FlushPkt())
		branches, err := repo.Branches()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
			return
		}
		if len(branches) == 0 {
			first := "0000000000000000000000000000000000000000 capabilities^{}\x00agent=forgesi-knot/0.1\n"
			_, _ = w.Write(packline.Pkt(first))
		} else {
			names := sortedKeys(branches)
			for i, n := range names {
				line := fmt.Sprintf("%s refs/heads/%s", branches[n], n)
				if i == 0 {
					line += "\x00agent=forgesi-knot/0.1"
				}
				_, _ = w.Write(packline.Pkt(line + "\n"))
			}
		}
		_, _ = w.Write(packline.FlushPkt())
	case "git-receive-pack":
		// Push advertisement requires auth first (knot2 parity).
		if !k.gitPushAuthorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		_, _ = w.Write(packline.Pkt("# service=git-receive-pack\n"))
		_, _ = w.Write(packline.FlushPkt())
		branches, err := repo.Branches()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
			return
		}
		caps := "\x00report-status agent=forgesi-knot/0.1"
		if len(branches) == 0 {
			_, _ = w.Write(packline.Pkt("0000000000000000000000000000000000000000 capabilities^{}" + caps + "\n"))
		} else {
			for i, n := range sortedKeys(branches) {
				line := fmt.Sprintf("%s refs/heads/%s", branches[n], n)
				if i == 0 {
					line += caps
				}
				_, _ = w.Write(packline.Pkt(line + "\n"))
			}
		}
		_, _ = w.Write(packline.FlushPkt())
	default:
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "dumb http not supported; use smart http")
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gitPushAuthorizer is the lxm-scoped auth receive-pack demands.
type gitPushAuthorizer interface {
	AuthorizeGitPush(r *http.Request, aud string) string
}

func (k *Knot) gitPushAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if g, ok := k.opt.Auth.(gitPushAuthorizer); ok {
		if g.AuthorizeGitPush(r, k.did()) != "" {
			return true
		}
	}
	// Dev authorizer accepts any bearer (local only).
	if d, ok := k.opt.Auth.(DevAuthorizer); ok {
		if d.Authorize(r, k.did()) != "" {
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="knot"`)
	writeErr(w, http.StatusUnauthorized, "Unauthorized", "push requires service auth (lxm="+receivePackLxm+")")
	return false
}

func (k *Knot) handleUploadPack(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := k.repoFor(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", gitContentTypeResult)
	w.Header().Set("Cache-Control", "no-cache")
	pr := packline.NewReader(r.Body)
	first, kind, err := pr.Next()
	if err != nil || kind != "data" || !strings.HasPrefix(string(first), "command=") {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "expected a v2 command")
		return
	}
	command := strings.TrimSpace(strings.TrimPrefix(string(first), "command="))
	switch command {
	case "ls-refs":
		for {
			payload, kind, err := pr.Next()
			if err != nil || kind != "data" {
				break
			}
			_ = payload // arguments (peel, ref-prefix) — we list all
		}
		branches, err := repo.Branches()
		if err != nil {
			return
		}
		for _, n := range sortedKeys(branches) {
			_, _ = w.Write(packline.Pkt(fmt.Sprintf("%s refs/heads/%s\n", branches[n], n)))
		}
		if len(branches) > 0 {
			def := k.defaultBranch(branches)
			_, _ = w.Write(packline.Pkt(fmt.Sprintf("%s HEAD symref-target:refs/heads/%s\n", branches[def], def)))
		}
		_, _ = w.Write(packline.FlushPkt())
	case "fetch":
		// Arguments live after the delimiter: read every data packet
		// until the closing flush, collecting wants.
		var wants []string
		hadHaves := false
		for {
			payload, kind, err := pr.Next()
			if err != nil || kind == "flush" {
				break
			}
			if kind != "data" {
				continue // delimiter between capabilities and arguments
			}
			line := string(bytes.TrimRight(payload, "\n"))
			if strings.HasPrefix(line, "want ") {
				wants = append(wants, strings.Fields(line)[1])
			}
			if strings.HasPrefix(line, "have ") {
				hadHaves = true
			}
		}
		// A client that sent haves gets an acknowledgments section
		// first (empty — we declare no common objects and resend
		// everything reachable). Sections are delimited by 0001.
		if hadHaves {
			_, _ = w.Write(packline.Pkt("acknowledgments\n"))
			// 'ready' ends negotiation: the packfile follows.
			_, _ = w.Write(packline.Pkt("ready\n"))
			_, _ = w.Write(packline.DelimPkt())
		}
		objs, err := repo.WalkReachable(wants)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
			return
		}
		pack, err := packfile.Encode(objs)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
			return
		}
		// v2 fetch: 'packfile' section header, then the pack as
		// sideband pkt-lines (channel 1 = pack data), closed by a flush.
		_, _ = w.Write(packline.Pkt("packfile\n"))
		const chunk = 65514 // 65520 max minus length header and sideband byte
		for off := 0; off < len(pack); off += chunk {
			end := off + chunk
			if end > len(pack) {
				end = len(pack)
			}
			_, _ = w.Write(packline.EncodePkt(append([]byte{1}, pack[off:end]...)))
		}
		_, _ = w.Write(packline.FlushPkt())
	default:
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "unknown command "+command)
	}
}

func (k *Knot) defaultBranch(branches map[string]string) string {
	if _, ok := branches["main"]; ok {
		return "main"
	}
	if _, ok := branches["master"]; ok {
		return "master"
	}
	return sortedKeys(branches)[0]
}

func (k *Knot) handleReceivePack(w http.ResponseWriter, r *http.Request) {
	acc, repo, ok := k.repoFor(w, r)
	if !ok {
		return
	}
	editor := ""
	if g, ok := k.opt.Auth.(gitPushAuthorizer); ok {
		editor = g.AuthorizeGitPush(r, k.did())
	} else if d, ok2 := k.opt.Auth.(DevAuthorizer); ok2 {
		editor = d.Authorize(r, k.did())
	}
	if editor == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="knot"`)
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "push requires service auth")
		return
	}
	w.Header().Set("Content-Type", gitContentTypePackResult)
	w.Header().Set("Cache-Control", "no-cache")

	// command list until flush, then the raw packfile follows
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		body = gzipReader(body)
	}
	pr := packline.NewReader(body)
	type update struct{ ref, newOID string }
	var updates []update
	for {
		payload, kind, err := pr.Next()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "InvalidRequest", "bad command list")
			return
		}
		if kind == "flush" {
			break
		}
		if kind != "data" {
			continue
		}
		line := string(bytes.TrimRight(payload, "\n"))
		line = strings.SplitN(line, "\x00", 2)[0] // drop v0 capabilities
		f := strings.Fields(line)
		if len(f) >= 3 && strings.HasPrefix(f[2], "refs/") {
			updates = append(updates, update{ref: f[2], newOID: f[1]})
		}
	}
	// everything after the flush is the raw packfile
	packData, err := io.ReadAll(io.LimitReader(body, 64<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "read pack")
		return
	}
	objs, err := packfile.Decode(bytes.NewReader(packData))
	if err != nil {
		_, _ = w.Write(packline.Pkt("unpack " + err.Error() + "\n"))
		for _, u := range updates {
			_, _ = w.Write(packline.Pkt("ng " + u.ref + " bad pack\n"))
		}
		_, _ = w.Write(packline.FlushPkt())
		return
	}
	updateMap := map[string]string{}
	for _, u := range updates {
		updateMap[u.ref] = u.newOID
	}
	if err := repo.ApplyPush(objs, updateMap); err != nil {
		_, _ = w.Write(packline.Pkt("unpack " + err.Error() + "\n"))
		for _, u := range updates {
			_, _ = w.Write(packline.Pkt("ng " + u.ref + " " + err.Error() + "\n"))
		}
		_, _ = w.Write(packline.FlushPkt())
		return
	}
	_, _ = w.Write(packline.Pkt("unpack ok\n"))
	for _, u := range updates {
		_, _ = w.Write(packline.Pkt("ok " + u.ref + "\n"))
	}
	_, _ = w.Write(packline.FlushPkt())

	// Publish the refs snapshot record (rkey 'self') — the signal knots
	// emit for mirrors/indexers, and our own console's branch list.
	k.publishRefsRecord(acc, editor)
}

// publishRefsRecord writes org.tangled.git.refs (rkey 'self') with the
// repo's current refs in git show-ref format.
func (k *Knot) publishRefsRecord(acc *repoAccount, editor string) {
	repo := gitrepo.Repo{Be: k.storeBackend(), RefPrefix: "git/" + acc.Did + "/"}
	branches, err := repo.Branches()
	if err != nil {
		return
	}
	var sb strings.Builder
	for _, n := range sortedKeys(branches) {
		sb.WriteString(branches[n] + " refs/heads/" + n + "\n")
	}
	def := "main"
	if _, ok := branches["main"]; !ok {
		if len(sortedKeys(branches)) > 0 {
			def = sortedKeys(branches)[0]
		}
	}
	record := map[string]any{
		"$type":         "org.tangled.git.refs",
		"defaultBranch": def,
		"refs":          sb.String(),
		"createdAt":     time.Now().UTC().Format(time.RFC3339),
	}
	var wrote cob.Write
	var werr error
	if _, gerr := k.store.Get(acc.Did, "org.tangled.git.refs", "self"); gerr == nil {
		wrote, werr = k.store.Put(acc.Did, "org.tangled.git.refs", "self", record, "", editor)
	} else {
		wrote, werr = k.store.Create(acc.Did, "org.tangled.git.refs", "self", record, editor)
	}
	if werr == nil {
		k.emit(acc.Did, "commit", "org.tangled.git.refs", "self", wrote.CID, wrote.Change, record)
	}
}

// gzipReader transparently decompresses git's gzipped request bodies.
func gzipReader(r io.Reader) io.Reader {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return r // not gzip; pass through
	}
	return gz
}
