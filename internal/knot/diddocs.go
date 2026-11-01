package knot

import (
	"net/http"
	"strings"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
)

// didDocRoutes serves DID documents for the repo DIDs the knot mints:
// did:web:forgesi.org:repo:<name> resolves at
// https://forgesi.org/repo:<name>/did.json — the #tangled_knot service
// entry is how every atproto consumer finds the hosting knot.
// ResolveSelf answers DID documents for identities THIS knot hosts: its
// own did:web and every repo did:web below it. The worker cannot fetch
// its own origin (the request loops back into the same worker and dies on
// Cloudflare's recursion guard), so verification of self-issued tokens
// resolves in-process. Returns nil for foreign dids.
func (k *Knot) ResolveSelf(did string) *atauth.DIDDoc {
	if did != k.did() && !strings.HasPrefix(did, k.did()+":repo:") {
		return nil
	}
	doc := &atauth.DIDDoc{}
	if k.opt.MintKey != nil {
		doc.Keys = append(doc.Keys, atauth.DIDKey{
			ID:        did + "#knot_key",
			Type:      "EcdsaSecp256k1VerificationKey2019",
			Multibase: "z" + atauth.Base58Encode(k.opt.MintKey.PubKey().SerializeCompressed()),
		})
	}
	return doc
}

func (k *Knot) didDocRoutes() {
	k.mux.HandleFunc("GET /{repodid}/did.json", k.handleRepoDidDoc)
}

func (k *Knot) handleRepoDidDoc(w http.ResponseWriter, r *http.Request) {
	segment := r.PathValue("repodid")
	name := strings.TrimPrefix(segment, "repo:")
	if name == segment { // not a repo did path
		writeErr(w, http.StatusNotFound, "NotFound", "not a repo did document")
		return
	}
	did := "did:web:" + k.opt.Host + ":repo:" + name
	if _, ok := k.registry.byDID(did); !ok {
		writeErr(w, http.StatusNotFound, "NotFound", "no such repo")
		return
	}
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
