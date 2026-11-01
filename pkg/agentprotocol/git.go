package agentprotocol

import "time"

// SecretGitUsername is the optional Command.Secrets key carrying the HTTPS
// basic-auth user that goes with Secrets[SecretGitToken]. Absent means
// DefaultGitUsername.
const SecretGitUsername = "git_username"

// DefaultGitUsername is the basic-auth user used with a bare access token.
const DefaultGitUsername = "x-access-token"

// GitAuth is the credential a git clone (or ls-remote) authenticates with. The
// control plane mints it at send time (a GitHub App installation token lives an
// hour) and attaches it to the command as secrets; it is never persisted.
type GitAuth struct {
	// Username is the HTTPS basic-auth user ("x-access-token" for GitHub,
	// "oauth2" for GitLab, Gitea and Forgejo).
	Username string
	// Token is the basic-auth password; empty means an anonymous clone.
	Token string
	// ExpiresAt is when the token stops working (nil: it does not expire).
	ExpiresAt *time.Time
}
