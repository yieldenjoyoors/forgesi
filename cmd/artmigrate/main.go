// Command artmigrate pushes one knot repo's D1 storage (git refs and
// COB record refs, with their objects) into its per-repo Cloudflare
// Artifacts repo. Input is two JSON dumps from wrangler d1 execute:
//
//	wrangler d1 execute forgesi-knot --remote --json --command \
//	  "SELECT ref, oid FROM knot_refs WHERE substr(ref,1,N)='PREFIX1' OR substr(ref,1,M)='PREFIX2'" \
//	  > refs.json
//	wrangler d1 execute forgesi-knot --remote --json --command \
//	  "SELECT oid, content FROM knot_objects" > objects.json
//
// Refs are rewritten bare: git/<did>/refs/... -> refs/...,
// refs/cobs/<did>/... -> refs/cobs/... — the per-repo Artifacts layout.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/yieldenjoyoors/forgesi/internal/artifacts"
	"github.com/yieldenjoyoors/forgesi/internal/artifactstore"
)

func main() {
	account := flag.String("account", "", "cloudflare account id")
	token := flag.String("token", "", "cloudflare api token")
	ns := flag.String("ns", "forgesi", "artifacts namespace")
	repo := flag.String("repo", "", "knot repo NAME (the Artifacts repo name)")
	did := flag.String("did", "", "knot repo DID (the D1 ref prefix)")
	refsFile := flag.String("refs", "refs.json", "refs dump")
	objsFile := flag.String("objects", "objects.json", "objects dump")
	flag.Parse()
	if *account == "" || *token == "" || *repo == "" || *did == "" {
		fmt.Println("account, token, repo, did required")
		os.Exit(2)
	}
	ctx := context.Background()
	c := artifacts.New(artifacts.Config{AccountID: *account, Namespace: *ns, APIToken: *token})
	if err := c.EnsureNamespace(ctx, "eu"); err != nil {
		fmt.Println("namespace:", err)
		os.Exit(1)
	}
	if _, err := c.EnsureRepo(ctx, *repo); err != nil {
		fmt.Println("ensure repo:", err)
		os.Exit(1)
	}

	// wrangler d1 --json wraps rows: [{"results": [...]}]
	refs := unwrap[struct {
		Ref string `json:"ref"`
		OID string `json:"oid"`
	}](*refsFile)
	objs := unwrap[struct {
		OID     string          `json:"oid"`
		Content json.RawMessage `json:"content"`
	}](*objsFile)

	be := artifactstore.New(c, *repo)
	n := 0
	for _, o := range objs {
		raw := decodeContent(o.Content)
		if _, err := be.PutObject(raw); err != nil {
			fmt.Println("putobject:", err)
			os.Exit(1)
		}
		n++
	}
	fmt.Printf("cached %d objects\n", n)

	gitPrefix := "git/" + *did + "/"
	cobPrefix := "refs/cobs/" + *did + "/"
	updates := map[string]string{}
	for _, r := range refs {
		var name string
		switch {
		case strings.HasPrefix(r.Ref, gitPrefix):
			name = strings.TrimPrefix(r.Ref, gitPrefix)
		case strings.HasPrefix(r.Ref, cobPrefix):
			// scoped per-repo layout: refs/cobs/<coll>/<rkey> (no repo DID)
			name = "refs/cobs/" + strings.TrimPrefix(r.Ref, cobPrefix)
		default:
			continue
		}
		updates[name] = r.OID
	}
	for name, oid := range updates {
		fmt.Println("ref:", name, "->", oid[:8])
	}
	if len(updates) > 0 {
		if err := be.SetRefs(updates); err != nil {
			fmt.Println("setrefs:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("MIGRATED %s: %d objects, %d refs\n", *repo, n, len(updates))
}

// decodeContent handles wrangler d1 blob shapes: base64 string, plain
// string, or a JSON array of bytes.
func decodeContent(raw json.RawMessage) []byte {
	var b64 string
	if err := json.Unmarshal(raw, &b64); err == nil {
		if dec, err := base64.StdEncoding.DecodeString(b64); err == nil {
			return dec
		}
		return []byte(b64)
	}
	var arr []byte
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return raw
}

// unwrap parses wrangler's envelope: [{"results":[{"ref":...,"oid":...}]}]
func unwrap[T any](path string, _ ...string) []T {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("read", path, err)
		os.Exit(2)
	}
	var env []struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		fmt.Println("parse", path, err)
		os.Exit(2)
	}
	var out []T
	for _, e := range env {
		for _, r := range e.Results {
			var t T
			if err := json.Unmarshal(r, &t); err == nil {
				out = append(out, t)
			}
		}
	}
	return out
}
