package gitproviders

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// cloneURLMatchesLink reports whether gitURL names the repository the link
// points at, on the connection's host (or at one of the clone URLs the
// provider reports for it). Only then may the connection's credentials be sent
// to it: a service's URL is editable by developers who cannot read the
// connection's token.
func cloneURLMatchesLink(c db.GitConnection, drv driver, repo, gitURL string, reported ...string) bool {
	raw := strings.TrimSpace(gitURL)
	var u *url.URL
	web := false
	if strings.Contains(raw, "://") {
		var err error
		if u, err = url.Parse(raw); err != nil {
			return false
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http":
			if u.User != nil {
				return false
			}
			web = true
		case "ssh": // authenticates with a deploy key
		default: // file://, ftp://…: not a repository the connection's credentials can serve
			return false
		}
	}
	// An https forge is never reached over cleartext.
	downgrade := func(forge string) bool {
		return web && strings.EqualFold(u.Scheme, "http") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(forge)), "https://")
	}
	for _, cand := range append([]string{drv.cloneURL(c.BaseUrl, repo)}, reported...) {
		if cand != "" && !downgrade(cand) && services.SameGitRepo(raw, cand) {
			return true
		}
	}
	// Same host as the connection and same repository name.
	return web && !downgrade(c.BaseUrl) && strings.EqualFold(u.Host, hostOf(c.BaseUrl)) && strings.EqualFold(RepoFullNameFromURL(raw), repo)
}

// linkMatches checks gitURL against the link; when the static check fails it
// asks the provider for the repository's own clone URL (a forge may serve
// clones from another address than its API).
func (s *Service) linkMatches(ctx context.Context, c db.GitConnection, drv driver, repo, gitURL string) bool {
	if cloneURLMatchesLink(c, drv, repo, gitURL) {
		return true
	}
	if !drv.hasAPI() || c.AuthType == AuthWebhookOnly || c.Status == StatusPending {
		return false
	}
	_, cl, err := s.api(ctx, c)
	if err != nil {
		return false
	}
	r, err := drv.getRepo(ctx, cl, repo)
	if err != nil || r.CloneURL == "" {
		return false
	}
	return cloneURLMatchesLink(c, drv, repo, gitURL, r.CloneURL)
}

// anonymousScheme reports whether gitURL uses git://: a protocol without
// authentication, so a clone over it can never carry the connection's
// credentials and there is nothing to refuse.
func anonymousScheme(gitURL string) bool {
	u, err := url.Parse(strings.TrimSpace(gitURL))
	return err == nil && strings.EqualFold(u.Scheme, "git")
}

// CloneAuth returns the credentials to clone gitURL for a linked service: a
// GitHub App installation token restricted to that repository with read
// access to its contents (valid one hour), or the connection's access token.
// It is called when a deploy command is sent and its result is only ever
// attached to that command; nothing is stored. A service without a link (or on
// a connection without credentials, or a git:// URL) gets an empty GitAuth, an
// anonymous clone. For any other URL that is not the repository of the link the
// credentials are refused (409 git_link_mismatch).
func (s *Service) CloneAuth(ctx context.Context, serviceID uuid.UUID, gitURL string) (agentprotocol.GitAuth, error) {
	sg, err := s.q.GetServiceGit(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentprotocol.GitAuth{}, nil
	}
	if err != nil {
		return agentprotocol.GitAuth{}, fmt.Errorf("load git link: %w", err)
	}
	if anonymousScheme(gitURL) {
		return agentprotocol.GitAuth{}, nil // git:// cannot carry a credential: an anonymous clone
	}
	c, err := s.loadConnectionInternal(ctx, sg.ConnectionID)
	if err != nil {
		return agentprotocol.GitAuth{}, err
	}
	drv := driverFor(Provider(c.Provider))
	if !s.linkMatches(ctx, c, drv, sg.RepoFullName, gitURL) {
		return agentprotocol.GitAuth{}, apierr.Conflict("git_link_mismatch",
			"The service's git URL no longer points at the repository its git connection is linked to ("+sg.RepoFullName+"): unlink the connection or set the URL back.")
	}
	if c.Status == StatusPending {
		return agentprotocol.GitAuth{}, fmt.Errorf("git connection %q is not finished yet", c.Name)
	}
	switch c.AuthType {
	case AuthGitHubApp:
		name := sg.RepoFullName
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		tok, exp, err := s.mintInstallationToken(ctx, c, name)
		if err != nil {
			return agentprotocol.GitAuth{}, err
		}
		return agentprotocol.GitAuth{Username: drv.cloneUsername(), Token: tok, ExpiresAt: &exp}, nil
	case AuthToken:
		cr, err := s.creds(c)
		if err != nil {
			return agentprotocol.GitAuth{}, err
		}
		return agentprotocol.GitAuth{Username: drv.cloneUsername(), Token: cr.Token}, nil
	case AuthOAuth:
		tok, err := s.oauthToken(ctx, c)
		if err != nil {
			return agentprotocol.GitAuth{}, err
		}
		cr, _ := s.creds(c)
		return agentprotocol.GitAuth{Username: drv.cloneUsername(), Token: tok, ExpiresAt: cr.ExpiresAt}, nil
	}
	return agentprotocol.GitAuth{}, nil
}
