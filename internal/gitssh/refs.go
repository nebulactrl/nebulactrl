package gitssh

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
)

// ResolveRef lists the remote's refs (git ls-remote) and returns the commit SHA
// of branch (a branch, a tag, or "HEAD"). auth may be nil (anonymous).
func ResolveRef(ctx context.Context, rawURL, branch string, auth transport.AuthMethod) (string, error) {
	rem := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{rawURL}})
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", err
	}
	want := []plumbing.ReferenceName{plumbing.NewBranchReferenceName(branch), plumbing.NewTagReferenceName(branch)}
	if branch == "HEAD" {
		want = []plumbing.ReferenceName{plumbing.HEAD}
	}
	byName := map[plumbing.ReferenceName]plumbing.Hash{}
	for _, r := range refs {
		if r.Type() == plumbing.HashReference {
			byName[r.Name()] = r.Hash()
		}
	}
	for _, n := range want {
		// Annotated tags: prefer the peeled commit.
		if h, ok := byName[plumbing.ReferenceName(string(n)+"^{}")]; ok {
			return h.String(), nil
		}
		if h, ok := byName[n]; ok {
			return h.String(), nil
		}
	}
	return "", fmt.Errorf("branch %q not found in %s", branch, rawURL)
}
