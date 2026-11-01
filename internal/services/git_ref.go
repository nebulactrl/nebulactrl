package services

import "github.com/google/uuid"

// GitRef is the link between a git service and a repository of a git
// connection (see internal/gitproviders). The link and its auto-deploy
// settings are operational: they never bump the generation and are not part
// of a release.
type GitRef struct {
	ConnectionID   uuid.UUID `json:"connectionId"`
	Provider       string    `json:"provider" enum:"github,gitlab,gitea,forgejo,bitbucket"`
	ConnectionName string    `json:"connectionName"`
	// Repo is the repository's full name (owner/name).
	Repo string `json:"repo"`
	// AutoDeploy deploys the service on every push to its branch.
	AutoDeploy bool `json:"autoDeploy"`
	// WatchPaths restrict auto-deploy to pushes touching matching files
	// (monorepos); empty means every push.
	WatchPaths []string `json:"watchPaths"`
}
