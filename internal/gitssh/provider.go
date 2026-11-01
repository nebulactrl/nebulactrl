package gitssh

import "strings"

// DeployKeyLink points at the page where a repository's deploy keys are added.
type DeployKeyLink struct {
	// Provider is github, gitlab, bitbucket, gitea or forgejo.
	Provider string `json:"provider"`
	Label    string `json:"label"`
	URL      string `json:"url"`
}

// DeployKeyLinkFor returns the provider-specific "Add deploy key" link when the
// host is a well-known public instance. Self-hosted instances are not guessed.
func DeployKeyLinkFor(ep Endpoint) (DeployKeyLink, bool) {
	repo := ep.Repo()
	if repo == "" || strings.Count(repo, "/") < 1 {
		return DeployKeyLink{}, false
	}
	switch strings.ToLower(ep.Host) {
	case "github.com", "ssh.github.com":
		return DeployKeyLink{"github", "Add deploy key on GitHub", "https://github.com/" + repo + "/settings/keys/new"}, true
	case "gitlab.com", "altssh.gitlab.com":
		return DeployKeyLink{"gitlab", "Add deploy key on GitLab", "https://gitlab.com/" + repo + "/-/settings/repository#js-deploy-keys-settings"}, true
	case "bitbucket.org":
		return DeployKeyLink{"bitbucket", "Add access key on Bitbucket", "https://bitbucket.org/" + repo + "/admin/access-keys/"}, true
	case "codeberg.org":
		return DeployKeyLink{"forgejo", "Add deploy key on Codeberg", "https://codeberg.org/" + repo + "/settings/keys"}, true
	case "gitea.com":
		return DeployKeyLink{"gitea", "Add deploy key on Gitea", "https://gitea.com/" + repo + "/settings/keys"}, true
	}
	return DeployKeyLink{}, false
}
