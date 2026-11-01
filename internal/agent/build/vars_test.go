package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVarsDockerArgsKeepSecretsOutOfArgv(t *testing.T) {
	v := Vars{
		Args:    map[string]string{"B_ARG": "two", "A_ARG": "one"},
		Secrets: map[string]string{"NPM_TOKEN": "sekret-canary-12345"},
	}
	args := v.DockerArgs()
	require.Equal(t, []string{
		"--build-arg", "A_ARG=one", "--build-arg", "B_ARG=two",
		"--secret", "id=NPM_TOKEN,env=NCBS_NPM_TOKEN",
	}, args)
	require.NotContains(t, strings.Join(args, " "), "sekret-canary-12345", "a secret never appears in argv")

	// The value rides in the process environment under a mangled name.
	env := v.Env([]string{"PATH=/bin"})
	require.Contains(t, env, "NCBS_NPM_TOKEN=sekret-canary-12345")
	require.Contains(t, env, "PATH=/bin")
	require.Equal(t, []string{"PATH=/bin"}, Vars{}.Env([]string{"PATH=/bin"}))
}

func TestScanEnvTemplates(t *testing.T) {
	repo := t.TempDir()
	ctx := filepath.Join(repo, "app")
	require.NoError(t, os.MkdirAll(ctx, 0o755))
	write := func(dir, name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write(repo, ".env.example", "# the secret\nSECRET_KEY_BASE=changeme\nexport DATABASE_URL=\nAPP_NAME=\"My App\" # trailing\nbad key=1\nSECRET_KEY_BASE=dup\n")
	write(repo, ".env", "REAL_SECRET=hunter2\n")       // never read
	write(repo, ".env.local", "REAL_SECRET=hunter2\n") // never read
	write(ctx, ".env.sample", "EXTRA=1\n")

	files := ScanEnvTemplates(repo, ctx)
	require.Len(t, files, 2)
	require.Equal(t, ".env.example", files[0].Path)
	require.Equal(t, "app/.env.sample", files[1].Path)
	byKey := map[string]string{}
	for _, v := range files[0].Vars {
		byKey[v.Key] = v.Value
	}
	require.Equal(t, map[string]string{"SECRET_KEY_BASE": "changeme", "DATABASE_URL": "", "APP_NAME": "My App"}, byKey)
	require.Equal(t, "the secret", files[0].Vars[0].Comment)
	raw, _ := os.ReadFile(filepath.Join(repo, ".env"))
	require.Contains(t, string(raw), "hunter2")
	for _, f := range files {
		for _, v := range f.Vars {
			require.NotEqual(t, "REAL_SECRET", v.Key)
		}
	}

	// Oversized files are skipped.
	write(repo, ".env.template", strings.Repeat("A=1\n", 20000))
	for _, f := range ScanEnvTemplates(repo, "") {
		require.NotEqual(t, ".env.template", f.Path)
	}
}
