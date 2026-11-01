package agentprotocol

import "strings"

// Scan-only reads of a git source: the template env files (.env.example, …)
// without building or deploying anything. The control plane reads them through
// the git provider's API when it can and asks the agent otherwise (Bitbucket,
// generic HTTPS, SSH, a private repository behind a pasted token).

// KindEnvScan asks the agent to fetch a branch head (shallow, in memory, no
// worktree) and report its template env files. The result's Data is an
// EnvScanResult.
const KindEnvScan = "env_scan"

func init() { AllowedKinds[KindEnvScan] = true }

// EnvScanPayload is the persisted payload of an env_scan command. It carries no
// secret: the git token is Secrets[SecretGitToken] (and SecretGitUsername) and
// the deploy key Secrets[SecretGitSSHKey], attached at send time.
type EnvScanPayload struct {
	// ServiceID is set for a scan of an existing service, empty for the
	// preview of a service that does not exist yet.
	ServiceID string `json:"serviceId,omitempty"`
	// ScanID keys the send-time secrets of a preview (nothing is stored for a
	// service that does not exist yet).
	ScanID string `json:"scanId,omitempty"`
	GitURL string `json:"gitUrl"`
	// Branch is the branch to read; empty means the repository's default branch.
	Branch string `json:"branch,omitempty"`
	// BuildContext is the build context directory, scanned as well as the
	// repository root ("" and "." mean the root only).
	BuildContext string `json:"buildContext,omitempty"`
	HasToken     bool   `json:"hasToken,omitempty"`
	// ConfigPaths are project config files (nebula.toml and the attachments it
	// names) to read from the fetched commit, at most ScanMaxConfigPaths. Every
	// path must satisfy ValidConfigPath.
	ConfigPaths []string `json:"configPaths,omitempty"`
	// ConfigOnly skips the template env files: only ConfigPaths are read.
	ConfigOnly bool `json:"configOnly,omitempty"`
	GitSSH
}

// EnvScanResult is ResultFrame.Data for env_scan commands.
type EnvScanResult struct {
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
	// Commit is the commit the files were read from.
	Commit string        `json:"commit,omitempty"`
	Files  []EnvFileScan `json:"files,omitempty"`
	// ConfigFiles holds the content of the requested ConfigPaths that exist at
	// Commit as regular files of at most ScanMaxConfigFileBytes; absent paths
	// are simply missing from the map.
	ConfigFiles map[string][]byte `json:"configFiles,omitempty"`
}

// ConfigFileName is the project config file looked for in a repository.
const ConfigFileName = "nebula.toml"

// Limits of project config reads.
const (
	// ScanMaxConfigPaths is the most config files one scan reads: the project
	// file and its attachments.
	ScanMaxConfigPaths = 9
	// ScanMaxConfigFileBytes bounds one config file.
	ScanMaxConfigFileBytes = 256 << 10
)

// ValidConfigPath reports whether p is a clean repository-relative file path:
// slash separated, not absolute, no empty, "." or ".." segment, no backslash or
// control character, at most 240 bytes.
func ValidConfigPath(p string) bool {
	if p == "" || len(p) > 240 || p[0] == '/' || p[len(p)-1] == '/' {
		return false
	}
	for i := range len(p) {
		if p[i] == '\\' || p[i] < 0x20 || p[i] == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
