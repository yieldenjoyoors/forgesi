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
	workers.Serve(knot.New(knot.Options{
		Host:    strings.TrimPrefix(strings.TrimPrefix(envOr("KNOT_HOST", "knot.forgesi.org"), "https://"), "http://"),
		Version: envOr("KNOT_VERSION", "0.1.0"),
	}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
