// Package artifacts is the Cloudflare Artifacts client: the control
// plane (REST: namespaces, repos, scoped git tokens) and the git data
// plane (ls-remote, fetch, push over smart HTTP). Artifacts has no
// object-write REST — writes go through git — which is fine: we speak
// fluent git. This is the storage core the knot was always meant to
// sit on (docs/knot-on-cloudflare.md); D1 keeps only the registry,
// event log and run ledger.
package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/packfile"
	"github.com/yieldenjoyoors/forgesi/internal/packline"
)

const defaultAPIBase = "https://api.cloudflare.com/client/v4/accounts"

func (c Config) apiBase() string {
	if c.RESTBase != "" {
		return strings.TrimSuffix(c.RESTBase, "/") + "/accounts"
	}
	return defaultAPIBase
}

// Config resolves endpoints and credentials. APIToken is the account
// API token (worker secret); tokens minted for git are repo-scoped and
// separate.
type Config struct {
	AccountID string
	Namespace string
	APIToken  string
	HTTP      *http.Client
	// GitBase overrides the data-plane origin (tests point it at a
	// local server). Empty = the real Artifacts host.
	GitBase string
	// RESTBase overrides the control-plane origin (tests). Empty =
	// api.cloudflare.com.
	RESTBase string
}

// Client talks to one namespace.
type Client struct {
	cfg Config
}

func New(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{cfg: cfg}
}

// GitURL is the repo's smart-HTTP remote.
func (c *Client) GitURL(repo string) string {
	if c.cfg.GitBase != "" {
		return strings.TrimSuffix(c.cfg.GitBase, "/") + "/git/" + c.cfg.Namespace + "/" + repo + ".git"
	}
	return fmt.Sprintf("https://%s.artifacts.cloudflare.net/git/%s/%s.git",
		c.cfg.AccountID, c.cfg.Namespace, repo)
}

func (c *Client) reposBase() string {
	return fmt.Sprintf("%s/%s/artifacts/namespaces/%s/repos", c.cfg.apiBase(), c.cfg.AccountID, c.cfg.Namespace)
}

func (c *Client) call(ctx context.Context, method, u string, body any) (int, map[string]any, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = b
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, err
	}
	var v map[string]any
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		_ = json.Unmarshal(raw, &v)
	}
	return res.StatusCode, v, nil
}

func result(v map[string]any) map[string]any {
	if r, ok := v["result"].(map[string]any); ok {
		return r
	}
	return v
}

func errOf(status int, v map[string]any, what string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	msg := fmt.Sprintf("HTTP %d", status)
	if v != nil {
		if errs, ok := v["errors"].([]any); ok && len(errs) > 0 {
			if e, ok := errs[0].(map[string]any); ok {
				if m, _ := e["message"].(string); m != "" {
					msg = m
				}
			}
		}
	}
	return fmt.Errorf("artifacts %s: %s", what, msg)
}

// EnsureNamespace creates the namespace once; an existing one is fine.
func (c *Client) EnsureNamespace(ctx context.Context, jurisdiction string) error {
	if jurisdiction == "" {
		jurisdiction = "eu"
	}
	u := fmt.Sprintf("%s/%s/artifacts/namespaces", c.cfg.apiBase(), c.cfg.AccountID)
	status, v, err := c.call(ctx, http.MethodPost, u, map[string]any{"namespace": c.cfg.Namespace, "jurisdiction": jurisdiction})
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	// already exists -> GET to confirm
	status2, _, err := c.call(ctx, http.MethodGet,
		fmt.Sprintf("%s/%s/artifacts/namespaces/%s", c.cfg.apiBase(), c.cfg.AccountID, c.cfg.Namespace), nil)
	if err != nil {
		return err
	}
	if status2 >= 200 && status2 < 300 {
		return nil
	}
	return errOf(status, v, "ensure namespace")
}

// EnsureRepo creates the repo; created=false when it already exists.
func (c *Client) EnsureRepo(ctx context.Context, name string) (bool, error) {
	status, v, err := c.call(ctx, http.MethodPost, c.reposBase(), map[string]any{"name": name})
	if err != nil {
		return false, err
	}
	if status >= 200 && status < 300 {
		return true, nil
	}
	status2, _, err := c.call(ctx, http.MethodGet, c.reposBase()+"/"+name, nil)
	if err != nil {
		return false, err
	}
	if status2 >= 200 && status2 < 300 {
		return false, nil
	}
	return false, errOf(status, v, "ensure repo "+name)
}

// MintToken mints a repo-scoped git token ("read" or "write").
func (c *Client) MintToken(ctx context.Context, repo, scope string, ttlSeconds uint64) (string, error) {
	u := fmt.Sprintf("%s/%s/artifacts/namespaces/%s/tokens", c.cfg.apiBase(), c.cfg.AccountID, c.cfg.Namespace)
	status, v, err := c.call(ctx, http.MethodPost, u, map[string]any{
		"scope": scope, "repo": repo, "ttl": ttlSeconds,
	})
	if err != nil {
		return "", err
	}
	if err := errOf(status, v, "mint "+scope+" token"); err != nil {
		return "", err
	}
	r := result(v)
	raw, _ := r["plaintext"].(string)
	if raw == "" {
		return "", fmt.Errorf("artifacts mint: no token in response")
	}
	if t, _, ok := strings.Cut(raw, "?"); ok {
		return t, nil // strip ?expires= on one-time tokens
	}
	return raw, nil
}

// ---------- git data plane ----------

// Ref is one ls-remote entry.
type Ref struct {
	OID  string
	Name string
}

// LSRemote lists the repo's refs (v0 advertisement).
func (c *Client) LSRemote(ctx context.Context, repo, token string) ([]Ref, error) {
	u := c.GitURL(repo) + "/info/refs?service=git-upload-pack"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("ls-remote %s: %d %s", repo, res.StatusCode, b)
	}
	r := packline.NewReader(res.Body)
	var out []Ref
	for {
		payload, kind, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		if kind != "data" {
			continue // flush/delim
		}
		s := strings.TrimSpace(string(payload))
		if strings.HasPrefix(s, "#") { // service header
			continue
		}
		s = strings.SplitN(s, "\x00", 2)[0]
		oid, name, ok := strings.Cut(s, " ")
		if !ok || len(oid) != 40 {
			continue
		}
		out = append(out, Ref{OID: oid, Name: name})
	}
	return out, nil
}

// Push updates one ref (v0 receive-pack) carrying the pack bytes.
// oldOID zeros for a create.
func (c *Client) Push(ctx context.Context, repo, token, oldOID, newOID, ref string, pack []byte) error {
	var body bytes.Buffer
	body.Write(packline.EncodePkt([]byte(oldOID + " " + newOID + " " + ref + "\x00 report-status\n")))
	body.Write(packline.FlushPkt())
	if len(pack) > 0 {
		body.Write(pack)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.GitURL(repo)+"/git-receive-pack", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("push %s: %d %s", repo, res.StatusCode, raw[:min(len(raw), 200)])
	}
	if !strings.Contains(string(raw), "unpack ok") {
		return fmt.Errorf("push rejected: %s", raw[:min(len(raw), 300)])
	}
	return nil
}

// Fetch pulls the packs for the wanted oids (git protocol v2) and
// decodes them into framed objects, ready for a Backend's cache.
func (c *Client) Fetch(ctx context.Context, repo, token string, wants []string) (map[string][]byte, error) {
	var req bytes.Buffer
	req.Write(packline.Pkt("command=fetch\n"))
	req.Write(packline.Pkt("agent=forgesi-knot/0.1\n"))
	req.Write(packline.DelimPkt())
	for _, w := range wants {
		req.Write(packline.Pkt("want " + w + "\n"))
	}
	req.Write(packline.Pkt("done\n"))
	req.Write(packline.FlushPkt())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.GitURL(repo)+"/git-upload-pack", &req)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	res, err := c.cfg.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("fetch %s: %d %s", repo, res.StatusCode, b)
	}
	pr := packline.NewReader(res.Body)
	var pack bytes.Buffer
	inPack := false
	for {
		payload, kind, err := pr.Next()
		if err != nil || kind == "flush" {
			if kind == "flush" && inPack {
				break
			}
			if err != nil {
				break
			}
			continue
		}
		if kind != "data" {
			continue
		}
		if !inPack && bytes.HasPrefix(payload, []byte("packfile")) {
			inPack = true
			continue
		}
		if inPack && len(payload) > 0 && payload[0] == 1 { // sideband ch1
			pack.Write(payload[1:])
		}
	}
	if pack.Len() == 0 {
		return nil, fmt.Errorf("fetch %s: empty pack", repo)
	}
	objs, err := packfile.Decode(bytes.NewReader(pack.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: decode: %w", repo, err)
	}
	out := map[string][]byte{}
	for _, o := range objs {
		out[o.OID] = packfile.GitObject(packfile.TypeName(o.Type), o.Data)
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
