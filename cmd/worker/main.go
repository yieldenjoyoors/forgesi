//go:build js && wasm

// Command worker is the ForgeSI knot entrypoint: a Go wasm module served by
// the Workers runtime. Identity + COB record surface over D1; git transport
// and the firehose attach next.
package main

import (
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"

	workers "github.com/syumai/workers-go"
	"github.com/syumai/workers-go/cloudflare"
	"github.com/syumai/workers-go/cloudflare/d1"
	"github.com/syumai/workers-go/cloudflare/fetch"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/knot"
)

// httpRuntime is the fetch-backed client for outbound calls in wasm.
var httpRuntime = fetch.NewClient().HTTPClient(fetch.RedirectModeFollow)

func main() {
	db, err := openDB()
	if err != nil {
		panic("knot: no D1 binding: " + err.Error())
	}
	auth := knot.Authorizer(knot.RejectAuthorizer{})
	switch cloudflare.Getenv("KNOT_AUTH") {
	case "dev":
		// Local development only.
		auth = knot.DevAuthorizer{DID: envOr("KNOT_DEV_DID", "did:web:forgesi.org:dev")}
	case "service":
		auth = knot.ServiceAuth{V: &atauth.Verifier{ResolveDID: resolveDID, Now: time.Now}}
	}
	mintKey := loadMintKey()
	workers.Serve(knot.New(knot.Options{
		Host:    strings.TrimPrefix(strings.TrimPrefix(envOr("KNOT_HOST", "forgesi.org"), "https://"), "http://"),
		Version: envOr("KNOT_VERSION", "0.1.0"),
		Auth:    auth,
		DB:      db,
		MintKey: mintKey,
	}))
}

// resolveDID fetches DID documents: did:web from the origin's
// /.well-known/did.json, did:plc from the PLC directory.
func resolveDID(did string) (*atauth.DIDDoc, error) {
	var url string
	switch {
	case strings.HasPrefix(did, "did:web:"):
		host := strings.ReplaceAll(strings.TrimPrefix(did, "did:web:"), "%3A", ":")
		host = strings.ReplaceAll(host, ":", "/")
		url = "https://" + host + "/.well-known/did.json"
	case strings.HasPrefix(did, "did:plc:"):
		url = "https://plc.directory/" + did
	default:
		return nil, io.ErrUnexpectedEOF
	}
	client := &http.Client{Transport: httpRuntime.Transport, Timeout: 10 * time.Second}
	if httpRuntime.Transport == nil {
		client = httpRuntime
	}
	res, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return atauth.ParseDIDDoc(body)
}

// openDB resolves the D1 binding into a database/sql handle.
func openDB() (*sql.DB, error) {
	connector, err := d1.OpenConnector("DB")
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

// envOr reads worker vars. os.Getenv cannot see them in wasm — vars arrive
// through the Workers runtime.
func envOr(key, fallback string) string {
	if v := cloudflare.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadMintKey reads the KNOT_SIGNING_KEY secret (32-byte scalar hex) —
// the key that signs repo push tokens; its public half lands in repo DID
// documents.
func loadMintKey() *btcec.PrivateKey {
	hexKey := strings.TrimSpace(cloudflare.Getenv("KNOT_SIGNING_KEY"))
	if hexKey == "" {
		return nil
	}
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != 32 {
		return nil
	}
	priv, _ := btcec.PrivKeyFromBytes(raw)
	return priv
}
