package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStarterNode(t *testing.T) {
	npm := writeFiles(t, map[string]string{"package.json": `{"scripts":{"start":"node a.js","build":"tsc"}}`, "package-lock.json": "{}"})
	df, err := StarterDockerfile("node", npm)
	require.NoError(t, err)
	require.Contains(t, df, "FROM node:26.13.0-slim@sha256:e4db4c914bf8ad241bd24804fc25d272d89bad97e68cdc8a1414862790a02c39")
	require.Contains(t, df, "RUN npm ci")
	require.Contains(t, df, "RUN npm run build")
	require.Contains(t, df, `CMD ["npm","start"]`)

	pnpm := writeFiles(t, map[string]string{"package.json": `{"main":"srv.js"}`, "pnpm-lock.yaml": "", "srv.js": ""})
	df, err = StarterDockerfile("node", pnpm)
	require.NoError(t, err)
	require.Contains(t, df, "pnpm install --frozen-lockfile")
	require.NotContains(t, df, "run build")
	require.Contains(t, df, `CMD ["node","srv.js"]`)

	yarn := writeFiles(t, map[string]string{"package.json": `{"scripts":{"start":"x"}}`, "yarn.lock": ""})
	df, err = StarterDockerfile("node", yarn)
	require.NoError(t, err)
	require.Contains(t, df, "yarn install --frozen-lockfile")
	require.Contains(t, df, `CMD ["yarn","start"]`)

	_, err = StarterDockerfile("node", writeFiles(t, map[string]string{"README": ""}))
	require.ErrorContains(t, err, "package.json")
}

func TestStarterNodeIgnoresAMainThatLeavesTheBuildContext(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "evil.js"), nil, 0o600))
	ctx := writeFiles(t, map[string]string{"package.json": `{"main":"` + filepath.ToSlash(filepath.Join(outside, "evil.js")) + `"}`, "index.js": ""})
	df, err := StarterDockerfile("node", ctx)
	require.NoError(t, err)
	require.Contains(t, df, `CMD ["node","index.js"]`)

	parent := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(parent, "evil.js"), nil, 0o600))
	sub := filepath.Join(parent, "app")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "package.json"), []byte(`{"main":"../evil.js"}`), 0o600))
	df, err = StarterDockerfile("node", sub)
	require.NoError(t, err)
	require.NotContains(t, df, "CMD")

	require.NoError(t, os.Symlink(filepath.Join(outside, "evil.js"), filepath.Join(sub, "link.js")))
	require.False(t, exists(sub, "link.js"))
}

func TestStarterGo(t *testing.T) {
	root := writeFiles(t, map[string]string{"go.mod": "module x\n\ngo 1.22.1\n", "main.go": "package main\nfunc main(){}\n"})
	df, err := StarterDockerfile("go", root)
	require.NoError(t, err)
	require.Contains(t, df, "FROM golang:1.22-alpine AS build")
	require.Contains(t, df, "go build -trimpath -o /out/app .")
	require.Contains(t, df, "FROM alpine:3.22")

	cmd := writeFiles(t, map[string]string{"go.mod": "module x\n", "cmd/api/main.go": "package main\n", "lib/x.go": "package lib\n"})
	df, err = StarterDockerfile("go", cmd)
	require.NoError(t, err)
	require.Contains(t, df, "./cmd/api")
	require.Contains(t, df, "FROM golang:1.27-alpine")

	two := writeFiles(t, map[string]string{"go.mod": "module x\n", "cmd/a/main.go": "package main\n", "cmd/b/main.go": "package main\n"})
	_, err = StarterDockerfile("go", two)
	require.ErrorContains(t, err, "exactly one")
}

func TestStarterPythonRubyPHPStatic(t *testing.T) {
	df, err := StarterDockerfile("python", writeFiles(t, map[string]string{"requirements.txt": "flask", "app.py": ""}))
	require.NoError(t, err)
	require.Contains(t, df, "pip install -r requirements.txt")
	require.Contains(t, df, `CMD ["python","app.py"]`)
	_, err = StarterDockerfile("python", writeFiles(t, map[string]string{"x": ""}))
	require.Error(t, err)

	df, err = StarterDockerfile("ruby", writeFiles(t, map[string]string{"Gemfile": "", "config.ru": ""}))
	require.NoError(t, err)
	require.Contains(t, df, "bundle exec rackup")
	df, err = StarterDockerfile("ruby", writeFiles(t, map[string]string{"Gemfile": "", "bin/rails": ""}))
	require.NoError(t, err)
	require.Contains(t, df, "bin/rails server")

	df, err = StarterDockerfile("php", writeFiles(t, map[string]string{"composer.json": "{}", "public/index.php": ""}))
	require.NoError(t, err)
	require.Contains(t, df, "APACHE_DOCUMENT_ROOT=/var/www/html/public")
	require.Contains(t, df, "composer install --no-dev")

	df, err = StarterDockerfile("static", writeFiles(t, map[string]string{"dist/index.html": ""}))
	require.NoError(t, err)
	require.Contains(t, df, "COPY dist /usr/share/nginx/html")
	require.Contains(t, df, "listen ${PORT}")
	_, err = StarterDockerfile("static", writeFiles(t, map[string]string{"x": ""}))
	require.ErrorContains(t, err, "index.html")

	_, err = StarterDockerfile("cobol", t.TempDir())
	require.Error(t, err)
	require.Contains(t, StarterIgnore("static"), ".git")
}
