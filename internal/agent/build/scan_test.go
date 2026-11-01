package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scanRepo creates a repository with a template env file in the root and in
// api/, a real .env that must never be read, project config files and a second
// branch.
func scanRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(gitBin, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	write := func(p, s string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644))
	}
	run("init", "-q")
	write(".env.example", "# Postgres\nDATABASE_URL=\nSECRET_KEY=changeme\nAPP_NAME=demo\n")
	write("api/.env.sample", "API_TOKEN=\n")
	write(".env", "REAL_SECRET=hunter2\n")
	write("api/.env.local", "REAL_SECRET=hunter2\n")
	write("nebula.toml", "schema = 1\n")
	write("deploy/nebula.toml", "schema = 1\nproject = \"nested\"\n")
	write("big.toml", strings.Repeat("x", agentprotocol.ScanMaxConfigFileBytes+1))
	require.NoError(t, os.Symlink("nebula.toml", filepath.Join(dir, "linked.toml")))
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	run("checkout", "-q", "-b", "next")
	write(".env.example", "ONLY_ON_NEXT=1\n")
	run("add", "-A")
	run("commit", "-q", "-m", "second")
	run("checkout", "-q", "main")
	return dir
}

func TestScanEnvReadsTemplateFilesFromABranchHead(t *testing.T) {
	repo := scanRepo(t)
	b := &Builder{}

	out, err := b.ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "main", BuildContext: "api"})
	require.NoError(t, err)
	commit, files := out.Commit, out.Files
	require.Len(t, commit, 40)
	require.Len(t, files, 2)
	require.Equal(t, ".env.example", files[0].Path)
	require.Equal(t, "api/.env.sample", files[1].Path)
	require.Equal(t, "DATABASE_URL", files[0].Vars[0].Key)
	require.Equal(t, "Postgres", files[0].Vars[0].Comment)
	for _, f := range files {
		for _, v := range f.Vars {
			require.NotEqual(t, "REAL_SECRET", v.Key, "a real .env is never read")
		}
	}
	require.Empty(t, out.ConfigFiles, "no config path was asked for")

	// Another branch reads that branch's head, and the commit differs.
	next, err := b.ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "next"})
	require.NoError(t, err)
	require.NotEqual(t, commit, next.Commit)
	require.Len(t, next.Files, 1)
	require.Equal(t, "ONLY_ON_NEXT", next.Files[0].Vars[0].Key)

	// No branch: the repository's default (HEAD).
	def, err := b.ScanEnv(context.Background(), EnvScanRequest{GitURL: repo})
	require.NoError(t, err)
	require.Equal(t, commit, def.Commit)
}

func TestScanEnvReadsRequestedConfigFilesAtTheBranchHead(t *testing.T) {
	repo := scanRepo(t)
	b := &Builder{}

	out, err := b.ScanEnv(context.Background(), EnvScanRequest{
		GitURL: repo, Branch: "main", ConfigOnly: true,
		ConfigPaths: []string{"nebula.toml", "deploy/nebula.toml", "missing.toml", "linked.toml", "big.toml", "api"},
	})
	require.NoError(t, err)
	require.Len(t, out.Commit, 40)
	require.Empty(t, out.Files, "config-only reads skip the template env files")
	require.Equal(t, map[string][]byte{
		"nebula.toml":        []byte("schema = 1\n"),
		"deploy/nebula.toml": []byte("schema = 1\nproject = \"nested\"\n"),
	}, out.ConfigFiles, "absent paths, symlinks, directories and oversized files are not returned")

	both, err := b.ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "main", ConfigPaths: []string{"nebula.toml"}})
	require.NoError(t, err)
	require.NotEmpty(t, both.Files, "without ConfigOnly the template files are read too")
	require.Len(t, both.ConfigFiles, 1)

	for _, bad := range [][]string{{"../x"}, {"/etc/passwd"}, {"a//b"}, {"a/../b"}, {""}} {
		_, err := b.ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "main", ConfigPaths: bad})
		require.Error(t, err, "%q", bad)
	}
	_, err = b.ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "main", ConfigPaths: make([]string, agentprotocol.ScanMaxConfigPaths+1)})
	require.Error(t, err)
}

func TestScanEnvWritesNothingToDiskAndFailsClearly(t *testing.T) {
	repo := scanRepo(t)
	state := t.TempDir()
	_, err := (&Builder{StateDir: state}).ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "main"})
	require.NoError(t, err)
	entries, err := os.ReadDir(state)
	require.NoError(t, err)
	require.Empty(t, entries, "the scan fetches into memory: no build workspace is created")

	_, err = (&Builder{}).ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "nope"})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "git fetch"), err.Error())

	_, err = (&Builder{}).ScanEnv(context.Background(), EnvScanRequest{GitURL: repo, Branch: "main", BuildContext: "../x"})
	require.Error(t, err, "a build context outside the repository is refused")

	_, err = (&Builder{}).ScanEnv(context.Background(), EnvScanRequest{GitURL: "git@example.com:acme/shop.git"})
	require.Error(t, err, "an SSH URL without a deploy key is refused before any network access")
}

func TestScanConfigFilesLooksInTheRootAndTheBuildContext(t *testing.T) {
	root := t.TempDir()
	put := func(p, s string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, p), []byte(s), 0o644))
	}
	put("nebula.toml", "schema = 1\n")
	put("apps/web/nebula.toml", "schema = 1\nproject = \"web\"\n")

	paths, files := ScanConfigFiles(root, filepath.Join(root, "apps/web"))
	require.Equal(t, []string{"nebula.toml", "apps/web/nebula.toml"}, paths)
	require.Equal(t, map[string][]byte{
		"nebula.toml":          []byte("schema = 1\n"),
		"apps/web/nebula.toml": []byte("schema = 1\nproject = \"web\"\n"),
	}, files)

	paths, files = ScanConfigFiles(root, root)
	require.Equal(t, []string{"nebula.toml"}, paths, "a build context equal to the root adds no second candidate")
	require.Len(t, files, 1)

	paths, files = ScanConfigFiles(root, filepath.Join(root, "apps/none"))
	require.Equal(t, []string{"nebula.toml", "apps/none/nebula.toml"}, paths, "a missing file is reported as looked at, not found")
	require.Len(t, files, 1)
}

func TestScanConfigFilesNeverFollowsSymlinksOrReadsOversizedFiles(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "nebula.toml"), []byte("schema = 1\n"), 0o644))
	root := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "nebula.toml"), filepath.Join(root, "nebula.toml")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "apps")))

	paths, files := ScanConfigFiles(root, filepath.Join(root, "apps"))
	require.Equal(t, []string{"nebula.toml", "apps/nebula.toml"}, paths)
	require.Empty(t, files, "neither a symlinked file nor a file behind a symlinked directory is read")

	big := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(big, "nebula.toml"), []byte(strings.Repeat("x", agentprotocol.ScanMaxConfigFileBytes+1)), 0o644))
	_, files = ScanConfigFiles(big, big)
	require.Empty(t, files)
}
