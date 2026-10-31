package agentprotocol

// SecretGitSSHKey is the Command.Secrets key carrying the OpenSSH private key
// of a service's deploy key (set when GitSSH.HasSSHKey). Like every secret it
// is attached at send time and never persisted.
const SecretGitSSHKey = "git_ssh_key"

// GitSSH is the non-secret part of an SSH clone, embedded in GitSource and
// BuildPlanPayload. The private key is Secrets[SecretGitSSHKey].
type GitSSH struct {
	// HasSSHKey indicates Secrets[SecretGitSSHKey] carries the deploy key; the
	// clone URL is then an ssh:// or scp-style URL.
	HasSSHKey bool `json:"hasSshKey,omitempty"`
	// KnownHosts are the pinned SSH host keys as known_hosts lines
	// ("host type base64"). The agent trusts nothing else: a host key that is
	// not listed, or differs, fails the clone.
	KnownHosts []string `json:"knownHosts,omitempty"`
}
