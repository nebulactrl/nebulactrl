package build

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/envtemplate"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Vars are the build-time variables of a build. Args are non-secret and become
// `--build-arg`; Secrets become BuildKit secrets, whose values only ever live
// in the docker CLI's process environment (never argv, never the image).
type Vars struct {
	Args    map[string]string
	Secrets map[string]string
}

// SecretEnvPrefix prefixes the process-environment variable that carries a
// build secret to `--secret id=K,env=NCBS_K` (so a secret named PATH cannot
// clobber the CLI's own environment).
const SecretEnvPrefix = "NCBS_"

// DockerArgs returns `--build-arg K=V` (sorted) and `--secret id=K,env=NCBS_K`
// arguments. Secret values are not part of argv.
func (v Vars) DockerArgs() []string {
	var out []string
	for _, k := range sortedKeys(v.Args) {
		out = append(out, "--build-arg", k+"="+v.Args[k])
	}
	for _, k := range sortedKeys(v.Secrets) {
		out = append(out, "--secret", "id="+k+",env="+SecretEnvPrefix+k)
	}
	return out
}

// validate rejects a variable name that would break out of its
// `--build-arg K=V` or `--secret id=K,env=NCBS_K` argument: a comma adds
// secret options (src=, type=file), "=" and NUL break the process environment.
func (v Vars) validate() error {
	for _, m := range []map[string]string{v.Args, v.Secrets} {
		for k := range m {
			if k == "" || strings.ContainsAny(k, ",=\x00\r\n") {
				return fmt.Errorf("invalid build variable name %q", k)
			}
		}
	}
	return nil
}

// Env returns base extended with the secrets' process environment entries.
func (v Vars) Env(base []string) []string {
	if len(v.Secrets) == 0 {
		return base
	}
	out := append([]string(nil), base...)
	for _, k := range sortedKeys(v.Secrets) {
		out = append(out, SecretEnvPrefix+k+"="+v.Secrets[k])
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ScanEnvTemplates reads the template env files in the repository root and in
// the build context (when different). Files over 64 KiB are skipped and at most
// 500 variables are returned in total. It never follows symlinks out of the
// repository. The result is nil when nothing was found.
func ScanEnvTemplates(repoDir, contextDir string) []agentprotocol.EnvFileScan {
	var out []agentprotocol.EnvFileScan
	budget := agentprotocol.ScanMaxVars
	dirs := []string{repoDir}
	if contextDir != "" && contextDir != repoDir {
		dirs = append(dirs, contextDir)
	}
	for _, dir := range dirs {
		for _, name := range envtemplate.Files {
			if budget <= 0 {
				return out
			}
			p := filepath.Join(dir, name)
			st, err := os.Lstat(p)
			if err != nil || !st.Mode().IsRegular() || st.Size() > agentprotocol.ScanMaxFileBytes {
				continue
			}
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			vars := envtemplate.Parse(f, budget)
			_ = f.Close()
			if len(vars) == 0 {
				continue
			}
			budget -= len(vars)
			rel, err := filepath.Rel(repoDir, p)
			if err != nil {
				rel = name
			}
			out = append(out, agentprotocol.EnvFileScan{Path: filepath.ToSlash(rel), Vars: vars})
		}
	}
	return out
}

// ScanConfigFiles looks for the project config file (nebula.toml) in the
// repository root and, when different, in the build context. It returns the
// repository-relative paths it looked at and the content of those that exist
// as regular files of at most agentprotocol.ScanMaxConfigFileBytes. Symlinks
// are never followed, not even a symlinked directory on the way.
func ScanConfigFiles(repoDir, contextDir string) (paths []string, files map[string][]byte) {
	paths = []string{agentprotocol.ConfigFileName}
	if contextDir != "" && contextDir != repoDir {
		if rel, err := filepath.Rel(repoDir, contextDir); err == nil && rel != "." && filepath.IsLocal(rel) {
			paths = append(paths, filepath.ToSlash(filepath.Join(rel, agentprotocol.ConfigFileName)))
		}
	}
	root, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return paths, nil
	}
	for _, p := range paths {
		full := filepath.Join(repoDir, filepath.FromSlash(p))
		st, err := os.Lstat(full)
		if err != nil || !st.Mode().IsRegular() || st.Size() > agentprotocol.ScanMaxConfigFileBytes {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(full); err != nil || resolved != filepath.Join(root, filepath.FromSlash(p)) {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if files == nil {
			files = map[string][]byte{}
		}
		files[p] = data
	}
	return paths, files
}
