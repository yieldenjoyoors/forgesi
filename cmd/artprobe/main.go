// Command artprobe exercises the Artifacts core live: ensure namespace
// and repo, mint a write token, build a commit with the gitrepo engine,
// push it, and read it back over the wire. Run with the account env:
//
//	set -a; source /opt/data/.env; set +a
//	go run ./cmd/artprobe -account $CF_ACCOUNT_ID -token $CLOUDFLARE_API_TOKEN -repo probe
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/artifacts"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
	"github.com/yieldenjoyoors/forgesi/internal/packfile"
)

func main() {
	account := flag.String("account", "", "cloudflare account id")
	token := flag.String("token", "", "cloudflare api token")
	ns := flag.String("ns", "forgesi", "artifacts namespace")
	repo := flag.String("repo", "probe", "repo name")
	flag.Parse()
	if *account == "" || *token == "" {
		fmt.Println("account and token required")
		os.Exit(2)
	}
	ctx := context.Background()
	c := artifacts.New(artifacts.Config{AccountID: *account, Namespace: *ns, APIToken: *token})

	if err := c.EnsureNamespace(ctx, "eu"); err != nil {
		fmt.Println("namespace:", err)
		os.Exit(1)
	}
	fmt.Println("namespace ok:", *ns)

	created, err := c.EnsureRepo(ctx, *repo)
	if err != nil {
		fmt.Println("repo:", err)
		os.Exit(1)
	}
	fmt.Println("repo ok (created:", created, "):", *repo)

	writeTok, err := c.MintToken(ctx, *repo, "write", 900)
	if err != nil {
		fmt.Println("mint write:", err)
		os.Exit(1)
	}
	fmt.Println("write token minted")

	// build one commit on an in-memory repo: a README at main
	be := cob.NewMemoryBackend()
	gr := gitrepo.Repo{Be: be}
	commit, err := gr.CommitFileOn("main", "", "PROBE.md", []byte("# artifacts probe\n\npushed "+time.Now().UTC().Format(time.RFC3339)+"\n"),
		"probe: artifacts core", "artprobe <artprobe@forgesi.org>")
	if err != nil {
		fmt.Println("commit:", err)
		os.Exit(1)
	}
	objs, err := gr.WalkReachable([]string{commit})
	if err != nil {
		fmt.Println("walk:", err)
		os.Exit(1)
	}
	pack, err := packfile.Encode(objs)
	if err != nil {
		fmt.Println("pack:", err)
		os.Exit(1)
	}
	fmt.Printf("built commit %s (%d objects, pack %d bytes)\\n", commit[:8], len(objs), len(pack))

	if err := c.Push(ctx, *repo, writeTok, "0000000000000000000000000000000000000000", commit, "refs/heads/main", pack); err != nil {
		fmt.Println("push:", err)
		os.Exit(1)
	}
	fmt.Println("pushed refs/heads/main ->", commit[:8])

	readTok, err := c.MintToken(ctx, *repo, "read", 300)
	if err != nil {
		fmt.Println("mint read:", err)
		os.Exit(1)
	}
	refs, err := c.LSRemote(ctx, *repo, readTok)
	if err != nil {
		fmt.Println("ls-remote:", err)
		os.Exit(1)
	}
	for _, r := range refs {
		fmt.Println("ref:", r.Name, r.OID[:8], "match:", r.OID == commit)
	}
	ok := false
	for _, r := range refs {
		if r.Name == "refs/heads/main" && r.OID == commit {
			ok = true
		}
	}
	if !ok {
		fmt.Println("PROBE FAILED: pushed commit not on refs/heads/main")
		os.Exit(1)
	}
	fmt.Println("ARTIFACTS CORE VERIFIED: create, mint, push, read back")
}
