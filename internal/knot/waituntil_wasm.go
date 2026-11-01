//go:build js

package knot

import "github.com/syumai/workers-go/cloudflare"

// The wasm build must register background work with the runtime — a bare
// goroutine is killed when the request's response completes.
var waitUntilHook = cloudflare.WaitUntil
