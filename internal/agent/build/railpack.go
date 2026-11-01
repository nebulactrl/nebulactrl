package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/pbkdf2"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Railpack runs in two steps, both against the pinned frontend image (one
// railpack version plans and builds, as the Railpack docs recommend):
//
//  1. `railpack prepare` writes railpack-plan.json. It runs in a throwaway,
//     unprivileged container created from the frontend image. The source is
//     copied in and the plan copied out with `docker cp`, so it works when the
//     agent itself runs in a container (no host bind mounts).
//  2. `docker build` with BUILDKIT_SYNTAX=<frontend image> and -f <plan>. The
//     plan's LLB needs BuildKit "mergeop", which Docker only enables with the
//     containerd image store. Without it the build runs through a managed
//     buildx builder (a BuildKit container) and is --load-ed into the daemon.

const (
	planPathInContainer = "/tmp/railpack-plan.json"
	infoPathInContainer = "/tmp/railpack-info.json"
	envPathInContainer  = "/tmp/nc-env.sh"
	prepareScript       = `set -a; if [ -f ` + envPathInContainer + ` ]; then . ` + envPathInContainer + `; fi; set +a; exec /railpack "$@"`
)

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// railpackPlan is what `railpack prepare` produced.
type railpackPlan struct {
	PlanFile string
	Info     agentprotocol.BuildInfo
	Messages []agentprotocol.PlanLog
	// Vars are the variables the plan was made with: the build's own plus the
	// runtime defaults (withRailpackDefaults). The plan names each as a BuildKit
	// secret, so the build must be given the same set.
	Vars map[string]string
}

// railpackInfo is the subset of railpack-info.json we read.
type railpackInfo struct {
	RailpackVersion  string `json:"railpackVersion"`
	ResolvedPackages map[string]struct {
		Name            string `json:"name"`
		ResolvedVersion string `json:"resolvedVersion"`
		Source          string `json:"source"`
	} `json:"resolvedPackages"`
	DetectedProviders []string `json:"detectedProviders"`
	Logs              []struct {
		Level string `json:"Level"`
		Msg   string `json:"Msg"`
	} `json:"logs"`
	Success bool `json:"success"`
}

// railpackPlanJSON is the subset of railpack-plan.json we read.
type railpackPlanJSON struct {
	Deploy struct {
		StartCommand string `json:"startCommand"`
	} `json:"deploy"`
	Steps []struct {
		Name     string `json:"name"`
		Commands []struct {
			Cmd string `json:"cmd"`
		} `json:"commands"`
	} `json:"steps"`
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// writeEnvScript writes vars as an `export`-able shell script (0600) so values
// with newlines or quotes survive and never appear in an argument vector.
// Names that are not valid shell identifiers are dropped and returned in skipped.
func writeEnvScript(path string, vars map[string]string) (keys, skipped []string, err error) {
	var buf bytes.Buffer
	for _, k := range sortedKeys(vars) {
		if !envKeyRE.MatchString(k) {
			skipped = append(skipped, k)
			continue
		}
		keys = append(keys, k)
		buf.WriteString(k + "=" + shellQuote(vars[k]) + "\n")
	}
	if len(keys) == 0 {
		return nil, skipped, nil
	}
	return keys, skipped, os.WriteFile(path, buf.Bytes(), 0o600)
}

// allVars merges plain and secret build variables: Railpack hands every
// variable to its build steps as a BuildKit secret.
func allVars(v Vars) map[string]string {
	out := make(map[string]string, len(v.Args)+len(v.Secrets))
	for k, val := range v.Args {
		out[k] = val
	}
	for k, val := range v.Secrets {
		out[k] = val
	}
	return out
}

// secretsHashSalt and secretsHashRounds fix the derivation, so one set of
// secrets always yields the same value (the layer cache depends on it).
const (
	secretsHashSalt   = "nebulactrl railpack secrets hash"
	secretsHashRounds = 100_000
)

// secretsHash changes whenever a secret value does, so BuildKit invalidates the
// layers that mounted it (Railpack's frontend turns it into a layer input). It
// travels as a build argument, which any local user can read from the process
// list while secret values themselves go through the environment; the key
// stretching keeps a weak secret from being guessed offline from this value.
func secretsHash(vars map[string]string) string {
	var in bytes.Buffer
	for _, k := range sortedKeys(vars) {
		in.WriteString(k)
		in.WriteByte(0)
		in.WriteString(vars[k])
		in.WriteByte(0)
	}
	return hex.EncodeToString(pbkdf2.Key(in.Bytes(), []byte(secretsHashSalt), secretsHashRounds, 16, sha256.New))
}

// prepareCreateArgs returns the `docker create` arguments of the plan step. The
// container name is unique per deployment.
func prepareCreateArgs(name string, keys []string) []string {
	args := []string{"create", "--name", name, "--entrypoint", "/bin/sh", agentprotocol.RailpackFrontendImage,
		"-c", prepareScript, "sh", "prepare", "/app", "--plan-out", planPathInContainer, "--info-out", infoPathInContainer}
	for _, k := range keys {
		args = append(args, "--env", k) // bare name: the value comes from the container environment
	}
	return args
}

// railpackPrepare plans the build of srcDir. workDir receives the plan files.
func (b *Builder) railpackPrepare(ctx context.Context, name, srcDir, workDir string, vars map[string]string, logf func(string)) (*railpackPlan, error) {
	lw := &LineWriter{Fn: logf}
	defer lw.Flush()
	run := func(args ...string) error { return b.run(ctx, args, nil, lw) }

	_ = b.run(ctx, []string{"rm", "-f", name}, nil, io.Discard) // a leftover of a crashed agent
	vars, defaulted := withRailpackDefaults(srcDir, vars)
	if len(defaulted) > 0 {
		logf("Railpack runtime defaults: " + strings.Join(defaulted, ", ") + " (pin your own with engines, .nvmrc, .node-version or a RAILPACK_*_VERSION build variable)")
	}
	envFile := filepath.Join(workDir, "nc-env.sh")
	keys, skipped, err := writeEnvScript(envFile, vars)
	if err != nil {
		return nil, fmt.Errorf("write build environment: %w", err)
	}
	if len(skipped) > 0 {
		logf("Skipping build variables whose names are not valid identifiers: " + strings.Join(skipped, ", "))
	}
	defer func() {
		_ = os.Remove(envFile)
		bg, cancel := detached(ctx)
		defer cancel()
		_ = b.run(bg, []string{"rm", "-f", name}, nil, io.Discard)
	}()

	logf("Planning the build with Railpack " + agentprotocol.RailpackVersion)
	if err := run(prepareCreateArgs(name, keys)...); err != nil {
		return nil, fmt.Errorf("railpack: create planner container: %w", err)
	}
	if err := run("cp", srcDir+string(filepath.Separator)+".", name+":/app"); err != nil {
		return nil, fmt.Errorf("railpack: copy source: %w", err)
	}
	if len(keys) > 0 {
		if err := run("cp", envFile, name+":"+envPathInContainer); err != nil {
			return nil, fmt.Errorf("railpack: copy build variables: %w", err)
		}
	}
	planFile := filepath.Join(workDir, "railpack-plan.json")
	infoFile := filepath.Join(workDir, "railpack-info.json")
	startErr := run("start", "-a", name)
	// The info file is written even when planning fails; it carries the reason.
	infoErr := run("cp", name+":"+infoPathInContainer, infoFile)
	var info railpackInfo
	if infoErr == nil {
		if raw, err := os.ReadFile(infoFile); err == nil {
			_ = json.Unmarshal(raw, &info)
		}
	}
	if startErr != nil {
		msg := "railpack could not plan this source"
		for _, l := range info.Logs {
			if l.Level == "error" {
				msg += ": " + l.Msg
				break
			}
		}
		return nil, fmt.Errorf("%s (%w)", msg, startErr)
	}
	if err := run("cp", name+":"+planPathInContainer, planFile); err != nil {
		return nil, fmt.Errorf("railpack: read plan: %w", err)
	}
	out, err := parsePlan(planFile, info)
	if err != nil {
		return nil, err
	}
	out.Vars = vars
	return out, nil
}

// parsePlan turns the plan and info files into a BuildInfo.
func parsePlan(planFile string, info railpackInfo) (*railpackPlan, error) {
	raw, err := os.ReadFile(planFile)
	if err != nil {
		return nil, fmt.Errorf("railpack: read plan: %w", err)
	}
	var plan railpackPlanJSON
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, fmt.Errorf("railpack: decode plan: %w", err)
	}
	out := &railpackPlan{PlanFile: planFile}
	bi := &out.Info
	bi.Method = agentprotocol.MethodRailpack
	bi.RailpackVersion = info.RailpackVersion
	if bi.RailpackVersion == "" {
		bi.RailpackVersion = agentprotocol.RailpackVersion
	}
	bi.Providers = info.DetectedProviders
	bi.StartCommand = plan.Deploy.StartCommand
	names := make([]string, 0, len(info.ResolvedPackages))
	for n := range info.ResolvedPackages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := info.ResolvedPackages[n]
		bi.Packages = append(bi.Packages, agentprotocol.BuildPackage{Name: p.Name, Version: p.ResolvedVersion, Source: p.Source})
	}
	for _, s := range plan.Steps {
		step := agentprotocol.BuildStep{Name: s.Name}
		for _, c := range s.Commands {
			if c.Cmd != "" {
				step.Commands = append(step.Commands, c.Cmd)
			}
		}
		bi.Steps = append(bi.Steps, step)
	}
	for _, l := range info.Logs {
		level := l.Level
		if level != "warn" && level != "suggestion" && level != "info" {
			continue
		}
		out.Messages = append(out.Messages, agentprotocol.PlanLog{Level: level, Msg: l.Msg})
	}
	return out, nil
}

// railpackBuildArgs is the `docker build` / `docker buildx build` argument
// vector of the build step. backend is "docker" or "buildx". Secret values are
// never part of it: they travel in the process environment (see Vars.Env).
func railpackBuildArgs(req Request, planFile, iidFile, ctxDir, backend string) []string {
	var args []string
	if backend == "buildx" {
		args = []string{"buildx", "build", "--builder", agentprotocol.BuildxBuilderName, "--load"}
	} else {
		args = []string{"build"}
	}
	args = append(args,
		"--progress=plain",
		"--build-arg", "BUILDKIT_SYNTAX="+agentprotocol.RailpackFrontendImage,
		"--build-arg", "cache-key="+req.railpackCacheKey(),
		"-f", planFile,
		"-t", ImageTag(req.ServiceSlug, req.DeploymentID),
		"--label", agentprotocol.LabelManaged+"=true",
		"--label", agentprotocol.LabelService+"="+req.ServiceID,
		"--iidfile", iidFile,
	)
	args = append(args, req.noCacheArgs()...)
	secrets := Vars{Secrets: allVars(req.Vars)}
	args = append(args, secrets.DockerArgs()...)
	if len(secrets.Secrets) > 0 {
		args = append(args, "--build-arg", "secrets-hash="+secretsHash(secrets.Secrets))
	}
	return append(args, ctxDir)
}

// railpackCacheKey is the cache-key build argument: Railpack prefixes the ids of
// its cache mounts with it. The daemon's BuildKit builder is shared by every
// project of the server and cache mounts are matched by id alone, so without a
// per-project key a Dockerfile `RUN --mount=type=cache,id=<railpack id>` of one
// project could fill the cache another project's install step trusts.
func (r Request) railpackCacheKey() string {
	key := r.CacheKey
	if key == "" {
		key = r.ServiceID
	}
	return "nebulactrl-" + strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			return c
		}
		return '-'
	}, key)
}

// usesContainerdStore reports whether `docker info` DriverStatus names the
// containerd snapshotter, the condition for BuildKit mergeop in the daemon.
func usesContainerdStore(driverStatusJSON string) bool {
	return strings.Contains(driverStatusJSON, "io.containerd.snapshotter")
}

// railpackBackend picks the BuildKit backend for Railpack builds.
func (b *Builder) railpackBackend(ctx context.Context) string {
	var out bytes.Buffer
	if err := b.run(ctx, []string{"info", "--format", "{{json .DriverStatus}}"}, nil, &out); err == nil && usesContainerdStore(out.String()) {
		return "docker"
	}
	return "buildx"
}

// ensureBuildx makes sure the managed buildx builder exists and is running.
func (b *Builder) ensureBuildx(ctx context.Context, logf func(string)) error {
	lw := &LineWriter{Fn: logf}
	defer lw.Flush()
	name := agentprotocol.BuildxBuilderName
	if err := b.run(ctx, []string{"buildx", "inspect", "--bootstrap", name}, nil, lw); err == nil {
		return nil
	}
	logf("Creating the BuildKit builder " + name + " (Railpack needs BuildKit features this Docker daemon's image store lacks)")
	if err := b.run(ctx, []string{"buildx", "create", "--name", name, "--driver", "docker-container",
		"--driver-opt", "image=" + agentprotocol.BuildKitImage, "--bootstrap"}, nil, lw); err != nil {
		return fmt.Errorf("create the BuildKit builder (is the docker buildx plugin installed?): %w", err)
	}
	return nil
}

// errNeedsMergeop recognises BuildKit refusing Railpack's LLB on a daemon
// without the containerd image store.
func errNeedsMergeop(tail string) bool { return strings.Contains(tail, "mergeop") }
