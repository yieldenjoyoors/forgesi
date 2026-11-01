//go:build !js

package knot

import (
	"net/http"
	"time"
)

// outboundHTTP is the client for knot-initiated outbound calls (runner
// wakes). The wasm build swaps it for the workers fetch transport.
var outboundHTTP = &http.Client{Timeout: 15 * time.Second}
