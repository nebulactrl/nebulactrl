package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadRepoFileReadsRegularFilesAndSymlinksInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("hello"), 0o644))
	require.NoError(t, os.Symlink("sub/a.txt", filepath.Join(root, "link.txt")))

	got, err := readRepoFile(root, "sub/a.txt")
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))
	got, err = readRepoFile(root, "link.txt")
	require.NoError(t, err)
	require.Equal(t, "hello", string(got), "a link that stays inside the checkout is followed")
}

// A checked-out symlink is attacker-controlled: package.json -> /dev/zero must
// neither be read (it would never end) nor read anything outside the checkout.
func TestReadRepoFileRefusesSymlinksOutOfTheCheckout(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("top secret"), 0o600))
	root := t.TempDir()
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(root, "package.json")))
	require.NoError(t, os.Symlink(secret, filepath.Join(root, ".nvmrc")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "dir")))

	for _, name := range []string{"package.json", ".nvmrc", "dir/secret", "../" + filepath.Base(outside) + "/secret"} {
		_, err := readRepoFile(root, name)
		require.Error(t, err, name)
	}
}

func TestReadRepoFileIsBounded(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.json"), []byte(strings.Repeat("a", maxRepoFileRead+1)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "ok.json"), []byte(strings.Repeat("a", maxRepoFileRead)), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "adir"), 0o755))

	_, err := readRepoFile(root, "big.json")
	require.ErrorContains(t, err, "larger than")
	_, err = readRepoFile(root, "ok.json")
	require.NoError(t, err)
	_, err = readRepoFile(root, "adir")
	require.ErrorIs(t, err, errNotRegularFile)
}

func TestRailpackDefaultsIgnoreASymlinkedPackageJSON(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(root, "package.json")))
	vars, applied := withRailpackDefaults(root, map[string]string{"A": "1"})
	require.Empty(t, applied, "an unreadable package.json is not a Node project")
	require.Equal(t, map[string]string{"A": "1"}, vars)
}

func TestStarterReadsAreConfinedToTheContext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(root, "package.json")))
	_, err := starterNode(root)
	require.ErrorContains(t, err, "needs a package.json")

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(root, "main.go")))
	require.False(t, hasMainPackage(root, "."))
}

func TestRedactGitURL(t *testing.T) {
	require.Equal(t, "https://github.com/a/b.git", redactGitURL("https://user:tok@github.com/a/b.git"))
	require.Equal(t, "https://github.com/a/b.git", redactGitURL("https://tok@github.com/a/b.git"))
	require.Equal(t, "git@github.com:a/b.git", redactGitURL("git@github.com:a/b.git"))
	require.Equal(t, "ssh://git@host/a/b.git", redactGitURL("ssh://git@host/a/b.git"))
	require.Equal(t, "https://github.com/a/b.git", redactGitURL("https://github.com/a/b.git"))
}

func TestCloneOptionsNeverSendATokenOverPlainHTTP(t *testing.T) {
	_, _, err := cloneOptions(Request{GitURL: "http://git.example.com/a/b.git", Token: "tok-123456"})
	require.ErrorContains(t, err, "plain http")

	opts, _, err := cloneOptions(Request{GitURL: "https://git.example.com/a/b.git", Token: "tok-123456"})
	require.NoError(t, err)
	require.NotNil(t, opts.Auth)

	opts, _, err = cloneOptions(Request{GitURL: "http://git.example.com/a/b.git"})
	require.NoError(t, err, "an anonymous clone over http is allowed")
	require.Nil(t, opts.Auth)
}

func TestRailpackBuildsAreKeyedPerProject(t *testing.T) {
	a, b := goodRequest(), goodRequest()
	a.CacheKey, b.CacheKey = "project-a", "project-b"
	argsA := strings.Join(railpackBuildArgs(a, "/w/plan.json", "/w/iid", "/w/src", "docker"), " ")
	argsB := strings.Join(railpackBuildArgs(b, "/w/plan.json", "/w/iid", "/w/src", "docker"), " ")
	require.Contains(t, argsA, "--build-arg cache-key=nebulactrl-project-a")
	require.Contains(t, argsB, "--build-arg cache-key=nebulactrl-project-b")

	none := goodRequest()
	require.Contains(t, strings.Join(railpackBuildArgs(none, "/w/plan.json", "/w/iid", "/w/src", "docker"), " "),
		"cache-key=nebulactrl-"+none.ServiceID, "without a project the service keys the cache")
	none.CacheKey = "a b/c"
	require.Equal(t, "nebulactrl-a-b-c", none.railpackCacheKey())
}
