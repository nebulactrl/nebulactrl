package agentprotocol

import "strings"

// Builders (Railpack, generated starter Dockerfiles) and the build-plan preview.

// KindBuildPlan asks the agent to clone a commit and report how it would be
// built, without building it. The result's Data is a BuildPlanResult.
const KindBuildPlan = "build_plan"

func init() { AllowedKinds[KindBuildPlan] = true }

// Build types (services.Config.BuildType). "starter:<lang>" selects a generated
// starter Dockerfile.
const (
	BuildAuto          = "auto"       // Dockerfile if present, else Railpack
	BuildDockerfile    = "dockerfile" // the repository's Dockerfile, required
	BuildRailpack      = "railpack"   // Railpack even when a Dockerfile exists
	BuildStarterPrefix = "starter:"
)

// Starter languages with a generated Dockerfile.
var StarterLangs = []string{"node", "python", "go", "ruby", "php", "static"}

// ValidBuildType reports whether t is a known build type. The empty string is
// not valid; callers default it to BuildAuto first.
func ValidBuildType(t string) bool {
	switch t {
	case BuildAuto, BuildDockerfile, BuildRailpack:
		return true
	}
	if lang, ok := strings.CutPrefix(t, BuildStarterPrefix); ok {
		for _, l := range StarterLangs {
			if l == lang {
				return true
			}
		}
	}
	return false
}

// Pinned Railpack. The frontend image carries the same railpack binary that
// plans the build (the docs recommend using one version for both), is pinned by
// the digest of its multi-arch index, and is what BUILDKIT_SYNTAX points at.
// To upgrade: bump all three constants together (see docs/operations/builds.md).
const (
	RailpackVersion       = "0.43.0"
	RailpackFrontendImage = "ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61fae6f2551103cd8b10c710cb07167dd353d561f15c69b8d7ac1c676cc"
	// BuildKitImage backs the managed buildx builder used when the Docker
	// daemon lacks the containerd image store (Railpack's LLB needs mergeop).
	BuildKitImage = "moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c"
	// BuildxBuilderName is the managed buildx builder's name.
	BuildxBuilderName = "nebulactrl-buildkit"
)

// Methods a build resolves to (BuildInfo.Method).
const (
	MethodDockerfile = "dockerfile"
	MethodRailpack   = "railpack"
	MethodStarter    = "starter"
)

// BuildPackage is one toolchain package Railpack resolved.
type BuildPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source,omitempty"`
}

// BuildStep is one step of a Railpack plan with its shell commands.
type BuildStep struct {
	Name     string   `json:"name"`
	Commands []string `json:"commands,omitempty"`
}

// BuildInfo describes how a source is (or was) built.
type BuildInfo struct {
	// Method is dockerfile, railpack or starter.
	Method string `json:"method"`
	// Reason is one sentence on why the method was chosen.
	Reason string `json:"reason,omitempty"`
	// Starter is the starter language (Method == starter).
	Starter string `json:"starter,omitempty"`
	// Dockerfile is the repository Dockerfile path (Method == dockerfile).
	Dockerfile string `json:"dockerfile,omitempty"`
	// RailpackVersion, Providers, Packages, Steps and StartCommand describe the
	// Railpack plan (Method == railpack).
	RailpackVersion string         `json:"railpackVersion,omitempty"`
	Providers       []string       `json:"providers,omitempty"`
	Packages        []BuildPackage `json:"packages,omitempty"`
	Steps           []BuildStep    `json:"steps,omitempty"`
	StartCommand    string         `json:"startCommand,omitempty"`
	// Builder is the BuildKit backend: "docker" (the daemon's own) or "buildx"
	// (the managed container builder). Railpack only.
	Builder string `json:"builder,omitempty"`
}

// BuildPlanPayload is the persisted payload of a build_plan command. It carries
// no secret values: the git token is Secrets[SecretGitToken] and build
// variables are Secrets["build:<KEY>"] as for deploys.
type BuildPlanPayload struct {
	ServiceID      string `json:"serviceId"`
	ServiceSlug    string `json:"serviceSlug"`
	GitURL         string `json:"gitUrl"`
	CommitSHA      string `json:"commitSha"`
	BuildType      string `json:"buildType"`
	DockerfilePath string `json:"dockerfilePath"`
	BuildContext   string `json:"buildContext"`
	HasToken       bool   `json:"hasToken,omitempty"`
	GitSSH
	BuildArgKeys    []string `json:"buildArgKeys,omitempty"`
	BuildSecretKeys []string `json:"buildSecretKeys,omitempty"`
	// Submodules is the submodule mode of the checkout (see GitSource.Submodules).
	Submodules string `json:"submodules,omitempty"`
}

// PlanLog is one Railpack planner message.
type PlanLog struct {
	// Level is info, warn or suggestion.
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// BuildPlanResult is ResultFrame.Data for build_plan commands.
type BuildPlanResult struct {
	Outcome   string `json:"outcome"`
	Message   string `json:"message,omitempty"`
	CommitSHA string `json:"commitSha,omitempty"`
	// Requested is the configured build type.
	Requested string `json:"requested"`
	// DockerfilePath and DockerfileFound report the Dockerfile lookup.
	DockerfilePath  string `json:"dockerfilePath"`
	DockerfileFound bool   `json:"dockerfileFound"`
	// SuggestedStarter is the starter language the sources look like, if any.
	SuggestedStarter string `json:"suggestedStarter,omitempty"`
	// Info is nil when the plan failed.
	Info     *BuildInfo `json:"info,omitempty"`
	Messages []PlanLog  `json:"messages,omitempty"`
}
