package deploykeys_test

import (
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func itoa(n int) string { return strconv.Itoa(n) }

// bareRepo makes a bare repository <root>/app.git with one commit on main.
func bareRepo(t *testing.T) (root, sha string) {
	t.Helper()
	root, work := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git(work, "init", "-q")
	git(work, "commit", "-q", "--allow-empty", "-m", "init")
	git(root, "clone", "-q", "--bare", work, "app.git")
	return root, git(work, "rev-parse", "HEAD")[:40]
}
