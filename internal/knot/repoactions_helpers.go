package knot

import "github.com/yieldenjoyoors/forgesi/internal/gitrepo"

// gitrepoRepo scopes the shared object pool to one repo's ref namespace.
func (k *Knot) gitrepoRepo(repoDID string) gitrepo.Repo {
	return gitrepo.Repo{Be: k.be, RefPrefix: "git/" + repoDID + "/"}
}

// mergeRepo is the route-level wrapper around the tree-merge engine.
func mergeRepo(gr gitrepo.Repo, base, head string) (string, bool, error) {
	return gitrepo.Merge(gr, base, head)
}
