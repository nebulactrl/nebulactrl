package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const gitResolveTimeout = 20 * time.Second

// The control plane resolves tenant-supplied git remotes: every http(s)
// connection of go-git goes through a client that refuses loopback, private,
// link-local and metadata addresses, also after a redirect (see netguard).
// It is installed at start, before any goroutine uses go-git.
func init() {
	tr := http.NewClient(netguard.NewClient(gitResolveTimeout))
	client.InstallProtocol("http", tr)
	client.InstallProtocol("https", tr)
}

// guardRemote refuses a git:// or SSH remote whose host is a private,
// loopback, link-local or metadata address (go-git dials those itself; the
// http(s) transport is guarded at connect time).
func guardRemote(ctx context.Context, rawURL string) error {
	var host string
	switch {
	case gitssh.LooksLikeSSH(rawURL):
		ep, err := gitssh.ParseURL(rawURL)
		if err != nil {
			return nil // validation reports a malformed URL
		}
		host = ep.Host
	case strings.HasPrefix(strings.ToLower(rawURL), "git://"):
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil
		}
		host = u.Hostname()
	default:
		return nil
	}
	return netguard.CheckHost(ctx, host)
}

// LsRemote resolves branch (or tag, or "HEAD") of rawURL to a commit SHA
// without cloning. token, when set, is sent as HTTPS basic auth with user
// (default `x-access-token`); it is never sent over plain http://. Errors never
// contain the token, credentials embedded in the URL or what the remote
// answered beyond its status.
func LsRemote(ctx context.Context, rawURL, branch, user, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitResolveTimeout)
	defer cancel()
	shown := services.RedactGitURL(rawURL)
	var auth transport.AuthMethod
	if token != "" {
		switch {
		case strings.HasPrefix(strings.ToLower(rawURL), "http://"):
			return "", fmt.Errorf("git ls-remote %s: an access token is never sent over plain http://: use an https:// URL", shown)
		case !strings.HasPrefix(strings.ToLower(rawURL), "git://"):
			if user == "" {
				user = agentprotocol.DefaultGitUsername
			}
			auth = &http.BasicAuth{Username: user, Password: token}
		}
	}
	if err := guardRemote(ctx, rawURL); err != nil {
		return "", fmt.Errorf("git ls-remote %s: %w", shown, err)
	}
	sha, err := gitssh.ResolveRef(ctx, rawURL, branch, auth)
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %s", shown, remoteError(err, token))
	}
	return sha, nil
}

// remoteError describes a failed ls-remote without what the remote sent:
// go-git copies the body of an error answer into its error, which would let
// a tenant read an internal service through the plan or build-plan messages.
func remoteError(err error, token string) string {
	var (
		he *http.Err
		ue *plumbing.UnexpectedError
	)
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired):
		return "authentication required"
	case errors.Is(err, transport.ErrAuthorizationFailed):
		return "authorization failed"
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return "repository not found"
	case errors.As(err, &ue) && errors.As(ue.Err, &he):
		return fmt.Sprintf("the remote answered HTTP %d", he.StatusCode())
	case errors.Is(err, netguard.ErrBlocked):
		return netguard.ErrBlocked.Error()
	}
	msg := err.Error()
	if token != "" {
		msg = strings.ReplaceAll(msg, token, services.Mask)
	}
	msg = services.RedactGitURL(msg)
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// SSHKeys supplies what a clone over SSH needs: the service's deploy key and
// the pinned host key of the git host (recorded on first use). It is
// implemented by deploykeys.Service.
type SSHKeys interface {
	// Credentials returns nil for a URL that is not an SSH URL and
	// gitssh.ErrNoKey when the service has no deploy key.
	Credentials(ctx context.Context, serviceID uuid.UUID, rawURL string) (*gitssh.Credentials, error)
	// Resolve is ls-remote over SSH; a changed host key is a *gitssh.MismatchError.
	Resolve(ctx context.Context, serviceID uuid.UUID, rawURL, branch string) (string, error)
}

// SetSSHKeys wires the SSH deploy keys of git services. Without it a service
// with an SSH git URL cannot be deployed.
func (s *Service) SetSSHKeys(k SSHKeys) { s.sshKeys = k }

// errSSHUnavailable is returned when a service has an SSH git URL but no SSH
// key store is wired.
var errSSHUnavailable = errors.New("SSH git URLs are not available on this control plane")

// sshCreds returns the SSH credentials for the clone of rawURL: nil for any
// other URL kind, an error when the service has no key or SSH is not wired.
func (s *Service) sshCreds(ctx context.Context, serviceID uuid.UUID, rawURL string) (*gitssh.Credentials, error) {
	if !gitssh.LooksLikeSSH(rawURL) {
		return nil, nil
	}
	if s.sshKeys == nil {
		return nil, errSSHUnavailable
	}
	return s.sshKeys.Credentials(ctx, serviceID, rawURL)
}

// sshCredsMessage is the text of an sshCreds failure that is safe to show to a
// user. Only the expected conditions (no key, SSH not wired) are spelled out; any
// other failure (database, decryption, host key scan) is logged and reduced to a
// generic message, since its text can carry internals and probe results.
func (s *Service) sshCredsMessage(ctx context.Context, serviceID uuid.UUID, err error) string {
	if errors.Is(err, gitssh.ErrNoKey) || errors.Is(err, errSSHUnavailable) {
		return err.Error()
	}
	s.Logger.ErrorContext(ctx, "load ssh deploy credentials", "service_id", serviceID.String(), "err", err)
	return "The SSH deploy key or host key could not be loaded; see the control plane log for details."
}

// errGitToken is returned when a stored git token cannot be decrypted.
var errGitToken = errors.New("decrypt git token")

// gitCreds are the credentials a clone of the service's repository uses.
type gitCreds struct {
	Username string
	Token    string
}

// attach adds the credentials to a command's send-time secrets (nothing for an
// anonymous clone).
func (c gitCreds) attach(secrets map[string]string) {
	if c.Token == "" {
		return
	}
	secrets[agentprotocol.SecretGitToken] = c.Token
	if c.Username != "" {
		secrets[agentprotocol.SecretGitUsername] = c.Username
	}
}

// gitCreds returns the clone credentials of gitURL: those of the service's git
// connection when it is linked to one (short-lived, minted now) and gitURL is
// the linked repository, otherwise the service's own stored token. The URL of
// a service is editable by people who cannot read the connection's token, so a
// URL that is not the linked repository gets no connection credentials (409
// git_link_mismatch).
func (s *Service) gitCreds(ctx context.Context, sv db.Service, gitURL string) (gitCreds, error) {
	if s.gitAuth != nil {
		if _, err := s.q.GetServiceGit(ctx, sv.ID); err == nil {
			a, err := s.gitAuth.CloneAuth(ctx, sv.ID, gitURL)
			if err != nil {
				if _, ok := apierr.As(err); ok {
					return gitCreds{}, err
				}
				return gitCreds{}, fmt.Errorf("git connection credentials: %w", err)
			}
			return gitCreds{Username: a.Username, Token: a.Token}, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return gitCreds{}, fmt.Errorf("load git link: %w", err)
		}
	}
	if len(sv.GitTokenEnc) == 0 {
		return gitCreds{}, nil
	}
	tok, err := s.Box.OpenString(secretbox.ServicesGitToken, sv.ID.String(), sv.GitTokenEnc)
	if err != nil {
		return gitCreds{}, fmt.Errorf("%w: %w", errGitToken, err)
	}
	return gitCreds{Token: tok}, nil
}

// hasGitAuth reports whether clones of the service are authenticated: it has a
// stored token or a git connection that supplies credentials.
func (s *Service) hasGitAuth(ctx context.Context, sv db.Service) bool {
	if len(sv.GitTokenEnc) > 0 {
		return true
	}
	if s.gitAuth == nil {
		return false
	}
	_, err := s.q.GetServiceGit(ctx, sv.ID)
	return err == nil
}

func (s *Service) resolveGit(ctx context.Context, sv db.Service, cfg services.Config) (string, error) {
	if gitssh.LooksLikeSSH(cfg.GitURL) {
		ctx, cancel := context.WithTimeout(ctx, gitResolveTimeout)
		defer cancel()
		if s.sshKeys == nil {
			return "", errors.New("SSH git URLs are not available on this control plane")
		}
		if err := guardRemote(ctx, cfg.GitURL); err != nil {
			return "", fmt.Errorf("git ls-remote %s: %w", services.RedactGitURL(cfg.GitURL), err)
		}
		return s.sshKeys.Resolve(ctx, sv.ID, cfg.GitURL, cfg.GitBranch)
	}
	c, err := s.gitCreds(ctx, sv, cfg.GitURL)
	if err != nil {
		return "", err
	}
	if s.lsRemote != nil {
		return s.lsRemote(ctx, cfg.GitURL, cfg.GitBranch, c.Username, c.Token)
	}
	return LsRemote(ctx, cfg.GitURL, cfg.GitBranch, c.Username, c.Token)
}

// SetGitResolver replaces the ls-remote of git sources (tests: the remote is a
// local fixture the guard would refuse).
func (s *Service) SetGitResolver(f func(ctx context.Context, url, branch, user, token string) (string, error)) {
	s.lsRemote = f
}
