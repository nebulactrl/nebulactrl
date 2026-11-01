package agentprotocol

// Build servers and registries: an agent with CapBuildPush builds a git source
// with BuildKit and pushes the image to an OCI registry (build_image). The
// target server then pulls it by digest with an ordinary deploy command.

const (
	// KindBuildImage clones a commit, builds it for one platform and pushes it
	// to a registry. Events (steps build, push) and log frames carry the
	// deployment ID, as for deploy. The result's Data is a BuildImageResult.
	// A cancel_deploy for the deployment ID stops a running build_image.
	KindBuildImage = "build_image"
	// KindRegistryCheck verifies registry reachability and credentials without
	// building. Its payload is a RegistryCheckPayload, the result's Data a
	// RegistryCheckResult.
	KindRegistryCheck = "registry_check"

	// CapBuildPush is advertised in Facts.Capabilities by agents that can build
	// and push to a registry (the docker buildx plugin is present). A missing
	// capability means "cannot".
	CapBuildPush = "build.push"
	// StepPush is the build_image step that pushes the image; the control plane
	// maps it to status building.
	StepPush = "push"
)

func init() {
	AllowedKinds[KindBuildImage] = true
	AllowedKinds[KindRegistryCheck] = true
}

// Send-time secrets of build_image and registry_check commands (never in the
// persisted payload). Git credentials use the existing SecretGitToken,
// SecretGitUsername and SecretGitSSHKey; build variables use
// SecretBuildPrefix + <KEY>.
const (
	// SecretRegistryPushAuth is base64url of a JSON RegistryAuth that may push.
	SecretRegistryPushAuth = "registry_push_auth"
)

// RegistryAuth is the decoded form of the registry credential secrets
// (SecretRegistryPushAuth, SecretRegistryAuth): the Docker X-Registry-Auth JSON.
type RegistryAuth struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	ServerAddress string `json:"serveraddress,omitempty"`
}

// BuildImagePayload is the persisted payload of a build_image command. It
// carries no secret value: the registry credential is
// Secrets[SecretRegistryPushAuth], git credentials are the SecretGit* keys and
// build variables are Secrets["build:<KEY>"].
type BuildImagePayload struct {
	DeploymentID string `json:"deploymentId"`
	ServiceID    string `json:"serviceId"`
	ProjectID    string `json:"projectId"`
	// Git is the source (URL, CommitSHA, DockerfilePath, BuildContext,
	// BuildType, HasToken, HasSSHKey, KnownHosts). ExistingImageID is ignored.
	Git GitSource `json:"git"`
	// Platform is the single target platform, e.g. linux/arm64.
	Platform string `json:"platform"`
	// Repository is host/ns/repo without a tag.
	Repository string `json:"repository"`
	// Tag is r-<release8>.
	Tag string `json:"tag"`
	// Builder names the per-project BuildKit builder, nebulactrl-buildkit-<project8>.
	Builder string `json:"builder"`
	// CacheRef is an optional registry cache ref; empty means local cache only.
	CacheRef string `json:"cacheRef,omitempty"`
	// BuildArgKeys and BuildSecretKeys list the build variables whose values
	// come in Secrets["build:<KEY>"].
	BuildArgKeys    []string `json:"buildArgKeys,omitempty"`
	BuildSecretKeys []string `json:"buildSecretKeys,omitempty"`
	// Insecure allows a plain-http registry (loopback and tests only; the
	// control plane refuses it for any other host).
	Insecure bool `json:"insecure,omitempty"`
}

// BuildImageResult is ResultFrame.Data of a build_image command.
type BuildImageResult struct {
	// Outcome is OutcomeSucceeded, OutcomeFailed or OutcomeCanceled.
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
	// Digest is the pushed manifest digest, sha256:….
	Digest     string `json:"digest,omitempty"`
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Platform   string `json:"platform,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	// Build describes how the source was built.
	Build *BuildInfo `json:"build,omitempty"`
	// CanceledAt is the step that was running when a canceled build stopped.
	CanceledAt string `json:"canceledAt,omitempty"`
}

// BuiltFrom records where a registry image was built (ImageSource.BuiltFrom),
// for display and cleanup protection.
type BuiltFrom struct {
	ReleaseID     string `json:"releaseId"`
	CommitSHA     string `json:"commitSha,omitempty"`
	BuildServerID string `json:"buildServerId,omitempty"`
}

// RegistryCheckPayload is the persisted payload of a registry_check command.
// The credential is Secrets[SecretRegistryPushAuth] (push and pull) or
// Secrets[SecretRegistryAuth] (pull only); without either the check is anonymous.
type RegistryCheckPayload struct {
	// Host is registry.example.com[:port], no scheme.
	Host     string `json:"host"`
	Insecure bool   `json:"insecure,omitempty"`
	// Repository, when set, scopes the token request of a bearer registry so
	// push permission can be verified (host/ns/repo or ns/repo).
	Repository string `json:"repository,omitempty"`
}

// RegistryCheckResult is ResultFrame.Data of a registry_check command. Push and
// Pull say what the supplied credentials were accepted for.
type RegistryCheckResult struct {
	Push  bool   `json:"push"`
	Pull  bool   `json:"pull"`
	Error string `json:"error,omitempty"`
}
