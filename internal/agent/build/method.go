package build

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DefaultDockerfilePath is the Dockerfile path that, when absent, lets the
// "auto" build type fall through to Railpack.
const DefaultDockerfilePath = "Dockerfile"

// Decision is how a checked-out source gets built.
type Decision struct {
	agentprotocol.BuildInfo
	// DockerfileAbs is the resolved Dockerfile (Method == dockerfile).
	DockerfileAbs string
	// DockerfileFound reports whether the configured Dockerfile exists.
	DockerfileFound bool
}

// Decide picks the build method for a checked-out repository:
//
//	auto        Dockerfile if present, else Railpack
//	dockerfile  the Dockerfile; its absence is an error
//	railpack    Railpack, even when a Dockerfile exists
//	starter:X   the generated starter Dockerfile for language X
//
// With "auto", a Dockerfile path the user changed from the default and that is
// missing is an error rather than a silent switch to Railpack.
func Decide(buildType, repoDir, dockerfilePath string) (Decision, error) {
	if buildType == "" {
		buildType = agentprotocol.BuildAuto
	}
	if !agentprotocol.ValidBuildType(buildType) {
		return Decision{}, fmt.Errorf("unknown build type %q", buildType)
	}
	if dockerfilePath == "" {
		dockerfilePath = DefaultDockerfilePath
	}
	var d Decision
	abs, err := resolveInside(repoDir, dockerfilePath)
	if errors.Is(err, errEscapes) {
		return d, fmt.Errorf("dockerfilePath: %w", err)
	}
	if err == nil {
		if st, serr := os.Stat(abs); serr == nil && !st.IsDir() {
			d.DockerfileFound, d.DockerfileAbs = true, abs
		}
	}

	switch {
	case buildType == agentprotocol.BuildDockerfile:
		if !d.DockerfileFound {
			return d, fmt.Errorf("dockerfilePath: %q not found in repository", dockerfilePath)
		}
		d.Method, d.Dockerfile = agentprotocol.MethodDockerfile, dockerfilePath
		d.Reason = "Build type is Dockerfile."
	case buildType == agentprotocol.BuildRailpack:
		d.Method = agentprotocol.MethodRailpack
		d.Reason = "Build type is Railpack."
		if d.DockerfileFound {
			d.Reason += " The repository's " + dockerfilePath + " is ignored."
		}
	case strings.HasPrefix(buildType, agentprotocol.BuildStarterPrefix):
		d.Method = agentprotocol.MethodStarter
		d.Starter = strings.TrimPrefix(buildType, agentprotocol.BuildStarterPrefix)
		d.Reason = "Build type is the " + d.Starter + " starter Dockerfile."
	default: // auto
		switch {
		case d.DockerfileFound:
			d.Method, d.Dockerfile = agentprotocol.MethodDockerfile, dockerfilePath
			d.Reason = dockerfilePath + " found in the repository."
		case dockerfilePath != DefaultDockerfilePath:
			return d, fmt.Errorf("dockerfilePath: %q not found in repository", dockerfilePath)
		default:
			d.Method = agentprotocol.MethodRailpack
			d.Reason = "No " + DefaultDockerfilePath + " in the repository, so Railpack detects how to build it."
		}
	}
	return d, nil
}

// DetectStarter suggests the starter language a source directory looks like,
// or "" when none matches. Order matters: a repository with a package.json and
// an index.html is a Node project.
func DetectStarter(dir string) string {
	has := func(names ...string) bool {
		for _, n := range names {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil && !st.IsDir() {
				return true
			}
		}
		return false
	}
	switch {
	case has("package.json"):
		return "node"
	case has("go.mod"):
		return "go"
	case has("requirements.txt", "pyproject.toml", "Pipfile", "setup.py"):
		return "python"
	case has("Gemfile"):
		return "ruby"
	case has("composer.json", "index.php"):
		return "php"
	case has("index.html"):
		return "static"
	}
	return ""
}
