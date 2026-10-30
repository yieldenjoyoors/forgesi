//go:build js && wasm

// Command worker is the ForgeSI knot entrypoint: a Go wasm module served by
// the Workers runtime. M1 exposes the knot identity surface; git transport,
// the COB store and the firehose attach in M2/M3.
package main

import (
	"os"
	"strings"

	workers "github.com/syumai/workers-go"

	"github.com/yieldenjoyoors/forgesi/internal/knot"
)

func main() {
	auth := knot.Authorizer(knot.RejectAuthorizer{})
	// KNOT_AUTH=dev: accept any bearer, attribute to KNOT_DEV_DID. Local
	// development only — the ES256K service-auth verifier replaces this.
	if envOr("KNOT_AUTH", "dev") == "dev" {
		auth = knot.DevAuthorizer{DID: envOr("KNOT_DEV_DID", "did:web:knot.forgesi.org:dev")}
	}
	workers.Serve(knot.New(knot.Options{
		Host:    strings.TrimPrefix(strings.TrimPrefix(envOr("KNOT_HOST", "knot.forgesi.org"), "https://"), "http://"),
		Version: envOr("KNOT_VERSION", "0.1.0"),
		Auth:    auth,
	}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
