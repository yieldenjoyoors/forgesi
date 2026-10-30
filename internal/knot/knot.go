// Package knot implements the ForgeSI knot: a Tangled-compatible git host
// speaking the AT Protocol from a Cloudflare Worker. Social data lives in
// repos as collaborative objects; code lives as ordinary git objects in
// Cloudflare Artifacts.
package knot

import (
	"encoding/json"
	"net/http"
	"strings"

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
}

// Knot serves the wire surface. Storage bindings attach in M2/M3; M1 is the
// identity layer every Tangled consumer probes first.
type Knot struct {
	opt      Options
	mux      *http.ServeMux
	store    *cob.Store
	registry *repoRegistry
}

// New builds the knot handler. A nil Auth fails closed: every write is
// 401 until a real authorizer is wired.
func New(opt Options) *Knot {
	if opt.Auth == nil {
		opt.Auth = RejectAuthorizer{}
	}
	st, err := cob.New(cob.NewMemoryBackend(), "did:web:"+opt.Host)
	if err != nil {
		panic(err) // construction cannot fail today; see cob.New
	}
	k := &Knot{opt: opt, mux: http.NewServeMux(), store: st, registry: newRepoRegistry()}
	k.routes()
	k.records()
	return k
}

func (k *Knot) routes() {
	k.mux.HandleFunc("GET /.well-known/did.json", k.handleDid)
	k.mux.HandleFunc("GET /xrpc/_health", k.handleHealth)
	k.mux.HandleFunc("GET /xrpc/org.tangled.knot.describeKnot", k.handleDescribeKnot)
}

func (k *Knot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mux.ServeHTTP(w, r)
}

// rejectAuthorizer denies everything.
type RejectAuthorizer struct{}

// Authorize implements Authorizer (always unauthenticated).
func (RejectAuthorizer) Authorize(*http.Request) string { return "" }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// didDocument is the knot's did:web identity. Consumers resolve the repo
// DID's doc for a TangledKnot service to find the hosting knot; the knot's
// own doc declares the same service type at its origin.
type didDocument struct {
	Context         string         `json:"@context"`
	ID              string         `json:"id"`
	VerificationMet []verifyMethod `json:"verificationMethod,omitempty"`
	Service         []didService   `json:"service"`
}

type verifyMethod struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Controller         string `json:"controller"`
	PublicKeyMultibase string `json:"publicKeyMultibase,omitempty"`
}

type didService struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

func (k *Knot) did() string {
	// did:web encodes the host with port digits percent-escaped; origins
	// without a port need no escaping.
	return "did:web:" + strings.ReplaceAll(k.opt.Host, ":", "%3A")
}

func (k *Knot) origin() string {
	return "https://" + k.opt.Host
}

func (k *Knot) handleDid(w http.ResponseWriter, r *http.Request) {
	doc := didDocument{
		Context: "https://www.w3.org/ns/did/v1",
		ID:      k.did(),
		Service: []didService{{
			ID:              k.did() + "#tangled_knot",
			Type:            "TangledKnot",
			ServiceEndpoint: k.origin(),
		}},
	}
	if k.opt.PublicKeyMultibase != "" {
		doc.VerificationMet = []verifyMethod{{
			ID:                 k.did() + "#knot_signing_key",
			Type:               "EcdsaSecp256k1VerificationKey2019",
			Controller:         k.did(),
			PublicKeyMultibase: k.opt.PublicKeyMultibase,
		}}
	}
	writeJSON(w, http.StatusOK, doc)
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
