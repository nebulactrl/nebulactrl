package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceDirStaysOneSegmentBelowBuilds(t *testing.T) {
	state := t.TempDir()
	for _, ok := range []string{"dep-1", "plan-0a1b2c3d-1.2", "a:b_c"} {
		got, err := workspaceDir(state, ok)
		require.NoError(t, err, ok)
		require.Equal(t, filepath.Join(state, "builds", ok), got)
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", "/abs", "a/../b", "x\\y", "-lead", ".hidden", "a\x00b", "a b"} {
		_, err := workspaceDir(state, bad)
		require.Error(t, err, "%q must be refused", bad)
	}
}

func TestCheckoutRefusesAnEscapingWorkspaceNameBeforeTouchingDisk(t *testing.T) {
	state := t.TempDir()
	keep := filepath.Join(state, "keep")
	require.NoError(t, os.WriteFile(keep, []byte("x"), 0o600))
	b := &Builder{StateDir: filepath.Join(state, "state"), Clone: func(context.Context, Request, string) error { return nil }}
	require.NoError(t, os.MkdirAll(filepath.Join(b.StateDir, "builds"), 0o700))
	_, _, _, _, err := b.checkout(context.Background(), Request{BuildContext: "."}, "../..", func(string) {})
	require.ErrorContains(t, err, "invalid workspace name")
	_, statErr := os.Stat(keep)
	require.NoError(t, statErr)
	_, statErr = os.Stat(b.StateDir)
	require.NoError(t, statErr)
}

func TestResolveInsideRefusesNonLocalPaths(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "app", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app", "Dockerfile"), []byte("FROM scratch"), 0o600))

	got, err := resolveInside(root, "app/Dockerfile")
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(filepath.Join(root, "app", "Dockerfile"))
	require.Equal(t, want, got)
	_, err = resolveInside(root, ".")
	require.NoError(t, err)
	_, err = resolveInside(root, "app/sub/../Dockerfile")
	require.NoError(t, err)

	for _, bad := range []string{"", "..", "../x", "app/../../x", "/etc/passwd"} {
		_, err := resolveInside(root, bad)
		require.ErrorIs(t, err, errEscapes, "%q", bad)
	}
}

func TestBuilderFileNeedsABuilderName(t *testing.T) {
	dir := t.TempDir()
	got, err := builderFile(dir, BuilderPrefix+"0a1b2c3d", ".toml")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, BuilderPrefix+"0a1b2c3d.toml"), got)

	for _, bad := range []string{"", "../x", BuilderPrefix + "../x", BuilderPrefix + "a/b", "other-0a1b", BuilderPrefix, "/abs"} {
		_, err := builderFile(dir, bad, ".toml")
		require.Error(t, err, "%q must be refused", bad)
	}
}
