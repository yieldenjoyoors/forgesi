//go:build !js || !wasm

package knot

import (
	"net/http"
)

// handleSubscribeRepos is wasm-only (the WebSocket upgrade needs the
// Workers runtime). Tests exercise the frame logic via getEvents.
func (k *Knot) handleSubscribeRepos(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "NotImplemented",
		"WebSocket firehose requires the Workers runtime; use getEvents for replay")
}
