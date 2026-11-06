package knot

import "github.com/yieldenjoyoors/forgesi/internal/gitrepo"

// gitrepoRepo scopes storage to one repo: per-repo Artifacts (bare
// refs) when configured, else the shared D1 pool (namespaced refs).
func (k *Knot) gitrepoRepo(repoDID string) gitrepo.Repo {
	return k.gitRepoFor(repoDID)
}

// mergeRepo is the route-level wrapper around the tree-merge engine.
func mergeRepo(gr gitrepo.Repo, base, head string) (string, bool, error) {
	return gitrepo.Merge(gr, base, head)
}
