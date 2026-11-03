// Package forgesi embeds the static console shell so the worker can
// serve SPA routes (/app/<owner-did>/<repo>) that the assets layer has
// no file for.
package forgesi

import _ "embed"

//go:embed web/app.html
var AppHTML []byte
