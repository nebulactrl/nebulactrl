package build

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// maxSubmoduleDepth bounds how deep nested submodules are followed.
const maxSubmoduleDepth = 5

// allowLocalSubmoduleURLs lets a .gitmodules point at a local path. It is false
// outside tests: a repository's .gitmodules is untrusted input and must not make
// the agent read files or run helpers on its own host.
var allowLocalSubmoduleURLs bool

// submoduleCheckout updates the submodules of wt recursively, after the commit
// is checked out.
//
// Credentials follow the parent repository and never leave its host: the HTTPS
// token (or the deploy key and its pinned host keys) is used only for a submodule
// on the same host, so a malicious .gitmodules cannot make the agent send them
// elsewhere. A scp-style or ssh:// submodule URL on the host of an HTTPS parent
// with a token is fetched over HTTPS with that token (the common
// `git@github.com:org/lib.git` in a repository built with a token). Anything else
// is fetched anonymously (https://, http://, git://) or refused (ssh to another
// host, file paths and other schemes).
//
// A submodule that cannot be fetched fails the checkout with mode on. With mode
// auto it is logged as a warning and skipped: a repository whose submodule is
// unreachable or optional must still build.
func submoduleCheckout(ctx context.Context, repo *git.Repository, wt *git.Worktree, req Request) error {
	a := &submoduleAuth{req: req, strict: req.Submodules == agentprotocol.SubmodulesOn, logf: req.Logf}
	if a.logf == nil {
		a.logf = func(string) {}
	}
	if gitssh.LooksLikeSSH(req.GitURL) {
		ep, err := gitssh.ParseURL(req.GitURL)
		if err != nil {
			return err
		}
		a.parentSSH = &ep
	} else if u, err := url.Parse(req.GitURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		a.parentHTTP = u
	}
	opts, explain, err := cloneOptions(req)
	if err != nil {
		return err
	}
	a.parentAuth, a.parentExplain = opts.Auth, explain
	return a.update(ctx, repo, wt, 1)
}

type submoduleAuth struct {
	req    Request
	strict bool
	logf   func(string)

	parentSSH     *gitssh.Endpoint
	parentHTTP    *url.URL
	parentAuth    transport.AuthMethod
	parentExplain func(error) error
}

// submoduleTarget is where and how one submodule is fetched.
type submoduleTarget struct {
	url     string
	auth    transport.AuthMethod
	explain func(error) error
}

func (a *submoduleAuth) update(ctx context.Context, repo *git.Repository, wt *git.Worktree, depth int) error {
	subs, err := wt.Submodules()
	if err != nil {
		if a.strict {
			return fmt.Errorf("read .gitmodules: %w", err)
		}
		a.logf("Warning: submodules skipped: cannot read .gitmodules: " + err.Error())
		return nil
	}
	if len(subs) == 0 {
		return nil
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		return err
	}
	for _, sub := range subs {
		cfg := sub.Config()
		if _, err := idx.Entry(cfg.Path); err != nil {
			// A .gitmodules entry without a gitlink in the commit: git ignores it too.
			a.logf(fmt.Sprintf("Submodule %s has no commit recorded in this checkout; skipped", cfg.Path))
			continue
		}
		if err := a.updateOne(ctx, sub, depth); err != nil {
			if a.strict || ctx.Err() != nil {
				return fmt.Errorf("submodule %s: %w", cfg.Path, err)
			}
			a.logf(fmt.Sprintf("Warning: submodule %s skipped: %v (set the service's submodules setting to \"off\" to silence this, or \"on\" to make it fail the build)", cfg.Path, err))
		}
	}
	return nil
}

func (a *submoduleAuth) updateOne(ctx context.Context, sub *git.Submodule, depth int) error {
	cfg := sub.Config()
	if err := checkSubmodulePath(cfg.Name, cfg.Path); err != nil {
		return err
	}
	tgt, err := a.target(cfg.URL)
	if err != nil {
		return err
	}
	cfg.URL = tgt.url
	a.logf(fmt.Sprintf("Fetching submodule %s from %s", cfg.Path, tgt.url))
	if err := sub.UpdateContext(ctx, &git.SubmoduleUpdateOptions{Init: true, Auth: tgt.auth}); err != nil {
		return tgt.explain(err)
	}
	if depth >= maxSubmoduleDepth {
		return nil
	}
	r, err := sub.Repository()
	if err != nil {
		return err
	}
	w, err := r.Worktree()
	if err != nil {
		return err
	}
	return a.update(ctx, r, w, depth+1)
}

// checkSubmodulePath rejects a submodule name or path that could point outside
// the worktree or into a .git directory.
func checkSubmodulePath(name, p string) error {
	for _, s := range []string{name, p} {
		if s == "" || strings.ContainsAny(s, "\x00\\") || strings.HasPrefix(s, "/") {
			return fmt.Errorf("invalid name or path %q", s)
		}
		for _, seg := range strings.Split(path.Clean(s), "/") {
			if seg == ".." || strings.EqualFold(seg, ".git") {
				return fmt.Errorf("invalid name or path %q", s)
			}
		}
	}
	return nil
}

// target decides the URL and credentials of one submodule (see submoduleCheckout).
func (a *submoduleAuth) target(raw string) (submoduleTarget, error) {
	raw = strings.TrimSpace(raw)
	plain := func(err error) error { return err }
	switch {
	case raw == "":
		return submoduleTarget{}, errors.New("the submodule has no url")
	case strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../"):
		// Relative to the parent's remote (go-git resolves it): same host, same credentials.
		return submoduleTarget{url: raw, auth: a.parentAuth, explain: a.parentExplain}, nil
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return submoduleTarget{}, fmt.Errorf("invalid url %q", raw)
		}
		if _, hasPw := u.User.Password(); hasPw {
			return submoduleTarget{}, errors.New("the url carries a password; credentials come from the service's source settings")
		}
		switch u.Scheme {
		case "https", "http":
			return submoduleTarget{url: raw, auth: a.httpAuth(u), explain: plain}, nil
		case "git":
			return submoduleTarget{url: raw, explain: plain}, nil
		case "ssh":
			return a.sshTarget(raw)
		}
		return submoduleTarget{}, fmt.Errorf("unsupported url scheme %q (use https://, or ssh on the repository's own host)", u.Scheme)
	case gitssh.LooksLikeSSH(raw):
		return a.sshTarget(raw)
	case allowLocalSubmoduleURLs:
		return submoduleTarget{url: raw, explain: plain}, nil
	}
	return submoduleTarget{}, fmt.Errorf("unsupported url %q", raw)
}

// httpAuth returns the parent's token for a submodule on the parent's host. The
// token is never sent in the clear to a host the parent does not use, nor
// downgraded from https.
func (a *submoduleAuth) httpAuth(u *url.URL) transport.AuthMethod {
	p := a.parentHTTP
	if a.req.Token == "" || p == nil || !strings.EqualFold(u.Host, p.Host) {
		return nil
	}
	if u.Scheme != "https" && u.Scheme != p.Scheme {
		return nil
	}
	user := a.req.TokenUser
	if user == "" {
		user = agentprotocol.DefaultGitUsername
	}
	return &githttp.BasicAuth{Username: user, Password: a.req.Token}
}

func (a *submoduleAuth) sshTarget(raw string) (submoduleTarget, error) {
	ep, err := gitssh.ParseURL(raw)
	if err != nil {
		return submoduleTarget{}, fmt.Errorf("invalid ssh url %q: %w", raw, err)
	}
	switch {
	case a.parentSSH != nil && ep.Addr() == a.parentSSH.Addr():
		auth, pins, err := gitssh.Credentials{PrivateKeyPEM: []byte(a.req.SSHKey), KnownHosts: a.req.KnownHosts}.Auth(ep)
		if err != nil {
			return submoduleTarget{}, err
		}
		return submoduleTarget{url: raw, auth: auth, explain: func(err error) error { return gitssh.Explain(err, ep, pins) }}, nil
	case a.parentHTTP != nil && a.req.Token != "" && strings.EqualFold(ep.Host, a.parentHTTP.Hostname()):
		u := &url.URL{Scheme: a.parentHTTP.Scheme, Host: a.parentHTTP.Host, Path: "/" + strings.TrimPrefix(ep.Path, "/")}
		return submoduleTarget{url: u.String(), auth: a.httpAuth(u), explain: func(err error) error { return err }}, nil
	}
	return submoduleTarget{}, fmt.Errorf("ssh url %q is on a host this build has no credentials for; use an https:// url", raw)
}
