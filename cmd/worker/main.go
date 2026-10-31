//go:build js && wasm

// Command worker is the ForgeSI knot entrypoint: a Go wasm module served by
// the Workers runtime. Identity + COB record surface over D1; git transport
// and the firehose attach next.
package main

import (
	"database/sql"
	"os"
	"strings"

	workers "github.com/syumai/workers-go"
	"github.com/syumai/workers-go/cloudflare/d1"

	"github.com/yieldenjoyoors/forgesi/internal/knot"
)

func main() {
	db, err := openDB()
	if err != nil {
		panic("knot: no D1 binding: " + err.Error())
	}
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
		DB:      db,
	}))
}

// openDB resolves the D1 binding into a database/sql handle.
func openDB() (*sql.DB, error) {
	connector, err := d1.OpenConnector("DB")
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
