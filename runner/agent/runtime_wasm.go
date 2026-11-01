//go:build js

package agent

import (
	"github.com/syumai/workers-go/cloudflare"
	"github.com/syumai/workers-go/cloudflare/fetch"
)

// The wasm build runs inside a Worker: outbound calls go over the
// workers fetch transport and env comes from the Worker bindings.
var httpRuntime = fetch.NewClient().HTTPClient(fetch.RedirectModeFollow)

func init() {
	httpClient = httpRuntime
	getEnv = cloudflare.Getenv
}
