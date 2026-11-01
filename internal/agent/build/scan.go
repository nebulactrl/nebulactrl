package build

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/nebulactrl/nebulactrl/internal/envtemplate"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// EnvScanRequest describes a scan-only read of a git source: its template env
// files and, optionally, project config files.
type EnvScanRequest struct {
	GitURL string
	// Branch is the branch to read; empty is the repository's default branch.
	Branch string
	// BuildContext is scanned as well as the repository root.
	BuildContext string
	// ConfigPaths are repository-relative files (agentprotocol.ValidConfigPath)
	// whose content is returned when they exist as regular files.
	ConfigPaths []string
	// ConfigOnly skips the template env files.
	ConfigOnly bool

	Token     string
	TokenUser string
	SSHKey    string
	// KnownHosts are the pinned SSH host keys (see Request.KnownHosts).
	KnownHosts []string
}

// EnvScanOutcome is what ScanEnv read.
type EnvScanOutcome struct {
	// Commit is the commit read.
	Commit string
	// Files are the template env files found.
	Files []agentprotocol.EnvFileScan
	// ConfigFiles are the requested config files that exist at Commit.
	ConfigFiles map[string][]byte
}

// ScanEnv reads the template env files (and the requested config files) of a
// branch head without building or checking anything out: a depth-1,
// single-branch fetch into memory (no worktree, nothing on disk), then only the
// wanted blobs are read from the commit's tree. It uses the same URL and
// credential handling as the build clone.
func (b *Builder) ScanEnv(ctx context.Context, req EnvScanRequest) (EnvScanOutcome, error) {
	if len(req.ConfigPaths) > agentprotocol.ScanMaxConfigPaths {
		return EnvScanOutcome{}, fmt.Errorf("at most %d config files can be read at once", agentprotocol.ScanMaxConfigPaths)
	}
	for _, p := range req.ConfigPaths {
		if !agentprotocol.ValidConfigPath(p) {
			return EnvScanOutcome{}, fmt.Errorf("invalid config path %q", p)
		}
	}
	opts, explain, err := cloneOptions(Request{
		GitURL: req.GitURL, Token: req.Token, TokenUser: req.TokenUser, SSHKey: req.SSHKey, KnownHosts: req.KnownHosts,
	})
	if err != nil {
		return EnvScanOutcome{}, err
	}
	opts.Depth, opts.SingleBranch = 1, true
	if req.Branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(req.Branch)
	}
	repo, err := git.CloneContext(ctx, memory.NewStorage(), nil, opts)
	if err != nil {
		return EnvScanOutcome{}, fmt.Errorf("git fetch: %w", explain(err))
	}
	head, err := repo.Head()
	if err != nil {
		return EnvScanOutcome{}, fmt.Errorf("resolve head: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return EnvScanOutcome{}, fmt.Errorf("read commit %s: %w", short(head.Hash().String()), err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return EnvScanOutcome{}, fmt.Errorf("read tree of %s: %w", short(head.Hash().String()), err)
	}
	out := EnvScanOutcome{Commit: head.Hash().String()}
	if !req.ConfigOnly {
		out.Files, err = envtemplate.Scan(req.BuildContext, func(p string) ([]byte, bool, error) {
			return readTreeFile(tree, p, agentprotocol.ScanMaxFileBytes)
		})
		if err != nil {
			return EnvScanOutcome{}, err
		}
	}
	for _, p := range req.ConfigPaths {
		data, ok, err := readTreeFile(tree, p, agentprotocol.ScanMaxConfigFileBytes)
		if err != nil {
			return EnvScanOutcome{}, err
		}
		if ok {
			if out.ConfigFiles == nil {
				out.ConfigFiles = map[string][]byte{}
			}
			out.ConfigFiles[p] = data
		}
	}
	return out, nil
}

// readTreeFile returns the content of the regular file p of tree. A missing
// path, a symlink (never followed) and a file larger than limit report found =
// false: an oversized file is not read at all.
func readTreeFile(tree *object.Tree, p string, limit int64) (data []byte, found bool, err error) {
	f, err := tree.File(p)
	if errors.Is(err, object.ErrFileNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !f.Mode.IsRegular() || f.Size > limit {
		return nil, false, nil
	}
	s, err := f.Contents()
	if err != nil {
		return nil, false, err
	}
	return []byte(s), true, nil
}
