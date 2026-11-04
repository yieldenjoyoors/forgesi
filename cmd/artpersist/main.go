// Command artpersist proves the artifactstore Backend against real
// Artifacts: write a commit through the backend (push), then read it
// back through a FRESH backend (cold cache, wire-only). The storage
// core end-to-end.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/yieldenjoyoors/forgesi/internal/artifacts"
	"github.com/yieldenjoyoors/forgesi/internal/artifactstore"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
)

func main() {
	account := flag.String("account", "", "cloudflare account id")
	token := flag.String("token", "", "cloudflare api token")
	repo := flag.String("repo", "persist", "repo name")
	flag.Parse()
	if *account == "" || *token == "" {
		fmt.Println("account and token required")
		os.Exit(2)
	}
	c := artifacts.New(artifacts.Config{
		AccountID: *account, Namespace: "forgesi", APIToken: *token,
	})
	ctx := context.Background()
	if err := c.EnsureNamespace(ctx, "eu"); err != nil {
		fmt.Println("namespace:", err)
		os.Exit(1)
	}
	if _, err := c.EnsureRepo(ctx, *repo); err != nil {
		fmt.Println("ensure repo:", err)
		os.Exit(1)
	}

	be := artifactstore.New(c, *repo)
	gr := gitrepo.Repo{Be: be}
	commit, err := gr.CommitFileOn("main", "", "PERSIST.md",
		[]byte("# persistence\n\nwritten through the artifactstore backend\n"), "persist: backend proof", "artpersist <artprobe@forgesi.org>")
	if err != nil {
		fmt.Println("commit:", err)
		os.Exit(1)
	}
	if err := be.SetRef("refs/heads/main", commit); err != nil {
		fmt.Println("setRef:", err)
		os.Exit(1)
	}
	fmt.Println("pushed commit", commit[:8])

	// cold backend: everything must come back over the wire
	be2 := artifactstore.New(c, *repo)
	oid, ok, err := be2.RefHead("refs/heads/main")
	if err != nil || !ok || oid != commit {
		fmt.Println("cold RefHead:", oid, ok, err)
		os.Exit(1)
	}
	files, err := gitrepo.Repo{Be: be2}.Materialize(commit)
	if err != nil {
		fmt.Println("cold Materialize:", err)
		os.Exit(1)
	}
	got := string(files["PERSIST.md"])
	if got != "# persistence\n\nwritten through the artifactstore backend\n" {
		fmt.Printf("cold content mismatch: %q\n", got)
		os.Exit(1)
	}
	fmt.Println("ARTIFACTSTORE BACKEND VERIFIED: push, cold readback, materialize")
}
