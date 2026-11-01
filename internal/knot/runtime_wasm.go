//go:build js

package knot

import "github.com/syumai/workers-go/cloudflare/fetch"

var outboundHTTP = fetch.NewClient().HTTPClient(fetch.RedirectModeFollow)
