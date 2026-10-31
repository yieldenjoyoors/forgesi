package knot

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
)

// mintToken issues a short-lived push token for a hosted repo: iss=repo
// DID, aud=knot DID, lxm=org.tangled.git.receivePack, signed with the
// knot's key. This is how agents (and CLIs) push without a PDS — the
// repo's DID document carries the knot's public key, so the standard
// verifier accepts the token on receive-pack.
func (k *Knot) handleMintToken(w http.ResponseWriter, r *http.Request) {
	if k.opt.MintKey == nil {
		writeErr(w, http.StatusNotImplemented, "NotImplemented", "knot has no signing key")
		return
	}
	editor := k.opt.Auth.Authorize(r, k.did())
	if editor == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="knot"`)
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return
	}
	var body struct {
		Repo string `json:"repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Repo == "" {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "missing repo")
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusNotFound, "NotFound", "no such repo")
		return
	}
	token, err := atauth.Mint(k.opt.MintKey, body.Repo, k.did(),
		"org.tangled.git.receivePack", time.Minute, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"exp":   time.Now().Add(time.Minute).Unix(),
	})
}

// registerMintRoutes is split so the route exists even without a key (501).
func (k *Knot) mintRoutes() {
	k.mux.HandleFunc("POST /xrpc/org.tangled.repo.mintToken", k.handleMintToken)
}

var _ = btcec.PrivateKey{}
