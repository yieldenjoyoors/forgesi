// Package knot implements the ForgeSI knot: a Tangled-compatible git host
// speaking the AT Protocol from a Cloudflare Worker. Social data lives in
// repos as collaborative objects; code lives as ordinary git objects in
// Cloudflare Artifacts.
package knot

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// Options configures the knot surface. Host is the public origin without a
// scheme (the knot's did:web identity is derived from it).
type Options struct {
	Host    string
	Version string
	// PublicKeyMultibase is the knot's signing key (secp256k1, compressed,
	// multibase base58btc). Empty disables the verificationMethod entry.
	PublicKeyMultibase string
	// Auth verifies write callers. DevAuthorizer for local development;
	// the ES256K service-auth verifier in production.
	Auth Authorizer
	// DB persists objects, refs and the repo registry (the D1 binding in
	// production; nil falls back to in-memory — unit tests only, since
	// isolates share nothing).
	DB *sql.DB
	// MintKey signs repo push tokens (org.tangled.repo.mintToken). When
	// set, repo DID documents carry its public half so standard service-auth
	// verification accepts minted tokens.
	MintKey *btcec.PrivateKey
	// Starter launches agent runs (the runner fetch in production). nil =
	// pull mode: runners ask fleet.nextRun.
	Starter func(run RunRequest) error
	// CronInternal marks the in-process cron caller (skips HTTP auth).
	CronInternal bool
	// RunnerKey authenticates fleet runners on nextRun/runDone (pull mode).
	RunnerKey string
	// ReviewLLM is the machine reviewer's model client (nil = no review).
	ReviewLLM func(system, user string) (string, error)
}

// Knot serves the wire surface. Storage bindings attach in M2/M3; M1 is the
// identity layer every Tangled consumer probes first.
type Knot struct {
	opt      Options
	mux      *http.ServeMux
	store    *cob.Store
	registry RepoStore
	be       cob.Backend // shared with the git transport
}

// New builds the knot handler. A nil Auth fails closed: every write is
// 401 until a real authorizer is wired. A nil DB falls back to in-memory
// storage (unit tests); production wires D1.
func New(opt Options) *Knot {
	if opt.Auth == nil {
		opt.Auth = RejectAuthorizer{}
	}
	var st *cob.Store
	var reg RepoStore
	var be cob.Backend
	if opt.DB != nil {
		if err := cob.Migrate(opt.DB); err != nil {
			panic("knot: migrate: " + err.Error())
		}
		be = cob.NewD1Backend(opt.DB)
		st, _ = cob.New(be, "did:web:"+opt.Host)
		reg = NewD1RepoStore(opt.DB)
	} else {
		be = cob.NewMemoryBackend()
		st, _ = cob.New(be, "did:web:"+opt.Host)
		reg = newMemoryRepoStore()
	}
	k := &Knot{opt: opt, mux: http.NewServeMux(), store: st, registry: reg, be: be}
	k.routes()
	k.records()
	k.gitRoutes()
	k.firehoseRoutes()
	k.didDocRoutes()
	k.wsRoutes()
	k.mintRoutes()
	k.fleetRoutes()
	k.mux.HandleFunc("POST /xrpc/org.tangled.repo.getFile", k.handleGetFile)
	k.mux.HandleFunc("POST /xrpc/org.tangled.repo.putFile", k.handlePutFile)
	k.mux.HandleFunc("POST /xrpc/org.tangled.git.merge", k.handleMerge)
	return k
}

func (k *Knot) routes() {
	k.mux.HandleFunc("GET /.well-known/did.json", k.handleKnotDidDoc)
	k.mux.HandleFunc("GET /xrpc/_health", k.handleHealth)
	k.mux.HandleFunc("GET /xrpc/org.tangled.knot.describeKnot", k.handleDescribeKnot)
}

func (k *Knot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mux.ServeHTTP(w, r)
}

// rejectAuthorizer denies everything.
type RejectAuthorizer struct{}

// Authorize implements Authorizer (always unauthenticated).
func (RejectAuthorizer) Authorize(*http.Request, string) string { return "" }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// didDocument is the knot's did:web identity. Consumers resolve the repo
// DID's doc for a TangledKnot service to find the hosting knot; the knot's
// own doc declares the same service type at its origin.
func (k *Knot) did() string {
	// did:web encodes the host with port digits percent-escaped; origins
	// without a port need no escaping.
	return "did:web:" + strings.ReplaceAll(k.opt.Host, ":", "%3A")
}

func (k *Knot) origin() string {
	return "https://" + k.opt.Host
}

func (k *Knot) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// describeKnotResponse is what the Tangled web client reads when a user adds
// the knot in Settings → Knots. Capabilities gate client features:
// knot-social-writes enables COB record writes, atproto-firehose the
// subscribeRepos stream.
type describeKnotResponse struct {
	Did          string   `json:"did"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// handleKnotDidDoc serves the knot's own DID document. It carries the
// signing key so service-auth tokens issued with iss = the knot DID (the
// knot acting as an editor — repo bootstrap, reviews) verify the standard
// way, and so the root doc resolves like any repo doc does.
func (k *Knot) handleKnotDidDoc(w http.ResponseWriter, r *http.Request) {
	did := k.did()
	doc := map[string]any{
		"@context": "https://www.w3.org/ns/did/v1",
		"id":       did,
		"service": []any{map[string]any{
			"id":              did + "#tangled_knot",
			"type":            "TangledKnot",
			"serviceEndpoint": k.origin(),
		}},
	}
	if k.opt.MintKey != nil {
		doc["verificationMethod"] = []any{map[string]any{
			"id":                 did + "#knot_key",
			"type":               "EcdsaSecp256k1VerificationKey2019",
			"controller":         did,
			"publicKeyMultibase": "z" + atauth.Base58Encode(k.opt.MintKey.PubKey().SerializeCompressed()),
		}}
	}
	writeJSON(w, http.StatusOK, doc)
}

func (k *Knot) handleDescribeKnot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, describeKnotResponse{
		Did:     k.did(),
		Version: k.Version(),
		Capabilities: []string{
			"knot-social-writes",
			"atproto-firehose",
		},
	})
}

// Version reports the knot software version.
func (k *Knot) Version() string {
	if k.opt.Version == "" {
		return "0.0.0"
	}
	return k.opt.Version
}
