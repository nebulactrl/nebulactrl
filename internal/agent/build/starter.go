package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Starter Dockerfiles are generated on the agent from what the build context
// contains. They are deliberately plain: official language images pinned to a
// major.minor tag, install from the lockfile, run the start script. They are an
// alternative to Railpack for people who want a Dockerfile they can read (the
// generated text is printed to the build log), and a base to copy into their
// own repository.
const (
	starterNodeImage   = "node:26.13.0-slim@sha256:e4db4c914bf8ad241bd24804fc25d272d89bad97e68cdc8a1414862790a02c39"
	starterPythonImage = "python:3.13-slim"
	starterGoImage     = "golang:%s-alpine"
	starterGoDefault   = "1.27"
	starterGoRuntime   = "alpine:3.22"
	starterRubyImage   = "ruby:3.4-slim"
	starterPHPImage    = "php:8.4-apache"
	starterComposer    = "composer:2"
	starterNginxImage  = "nginx:1.29-alpine"
)

// StarterDockerfile returns the generated Dockerfile for lang, written against
// the files in ctxDir (the build context).
func StarterDockerfile(lang, ctxDir string) (string, error) {
	switch lang {
	case "node":
		return starterNode(ctxDir)
	case "python":
		return starterPython(ctxDir)
	case "go":
		return starterGo(ctxDir)
	case "ruby":
		return starterRuby(ctxDir)
	case "php":
		return starterPHP(ctxDir)
	case "static":
		return starterStatic(ctxDir)
	}
	return "", fmt.Errorf("unknown starter %q", lang)
}

type dockerfile struct{ strings.Builder }

func (d *dockerfile) line(format string, a ...any) {
	fmt.Fprintf(&d.Builder, format+"\n", a...)
}

func exists(dir string, names ...string) bool {
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// cmdJSON renders a JSON-form CMD instruction.
func cmdJSON(argv ...string) string {
	b, _ := json.Marshal(argv)
	return "CMD " + string(b)
}

func starterNode(ctx string) (string, error) {
	raw, err := readRepoFile(ctx, "package.json")
	if err != nil {
		return "", errors.New("starter:node needs a package.json in the build context")
	}
	var pkg struct {
		Main    string            `json:"main"`
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("starter:node: package.json: %w", err)
	}
	install, run := "npm install", "npm run"
	pm := "npm"
	switch {
	case exists(ctx, "pnpm-lock.yaml"):
		install, run, pm = "corepack enable && pnpm install --frozen-lockfile", "pnpm run", "pnpm"
	case exists(ctx, "yarn.lock"):
		install, run, pm = "corepack enable && yarn install --frozen-lockfile", "yarn run", "yarn"
	case exists(ctx, "package-lock.json", "npm-shrinkwrap.json"):
		install = "npm ci"
	}
	var d dockerfile
	d.line("FROM %s", starterNodeImage)
	d.line("WORKDIR /app")
	d.line("COPY . .")
	d.line("RUN %s", install)
	if pkg.Scripts["build"] != "" {
		d.line("RUN %s build", run)
	}
	d.line("ENV NODE_ENV=production")
	switch {
	case pkg.Scripts["start"] != "":
		d.line("%s", cmdJSON(pm, "start"))
	case pkg.Main != "" && exists(ctx, pkg.Main):
		d.line("%s", cmdJSON("node", pkg.Main))
	case exists(ctx, "server.js"):
		d.line("%s", cmdJSON("node", "server.js"))
	case exists(ctx, "index.js"):
		d.line("%s", cmdJSON("node", "index.js"))
	}
	return d.String(), nil
}

func starterPython(ctx string) (string, error) {
	var d dockerfile
	d.line("FROM %s", starterPythonImage)
	d.line("WORKDIR /app")
	d.line("ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1")
	d.line("COPY . .")
	switch {
	case exists(ctx, "requirements.txt"):
		d.line("RUN pip install -r requirements.txt")
	case exists(ctx, "Pipfile.lock", "Pipfile"):
		d.line("RUN pip install pipenv && pipenv install --system --deploy")
	case exists(ctx, "pyproject.toml", "setup.py"):
		d.line("RUN pip install .")
	default:
		return "", errors.New("starter:python needs requirements.txt, Pipfile, pyproject.toml or setup.py in the build context")
	}
	for _, entry := range []string{"main.py", "app.py", "server.py"} {
		if exists(ctx, entry) {
			d.line("%s", cmdJSON("python", entry))
			break
		}
	}
	return d.String(), nil
}

var goDirective = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)
var goMainPkg = regexp.MustCompile(`(?m)^package\s+main\b`)

// hasMainPackage reports whether the directory rel (below root) holds a Go
// file of package main. Reads stay inside root and are bounded.
func hasMainPackage(root, rel string) bool {
	entries, err := readRepoDir(root, rel)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := readRepoFile(root, path.Join(rel, e.Name()))
		if err == nil && goMainPkg.Match(b) {
			return true
		}
	}
	return false
}

func starterGo(ctx string) (string, error) {
	mod, err := readRepoFile(ctx, "go.mod")
	if err != nil {
		return "", errors.New("starter:go needs a go.mod in the build context")
	}
	version := starterGoDefault
	if m := goDirective.FindSubmatch(mod); m != nil {
		version = string(m[1])
	}
	pkg := "."
	if !hasMainPackage(ctx, ".") {
		cmds, _ := readRepoDir(ctx, "cmd")
		var mains []string
		for _, e := range cmds {
			if e.IsDir() && hasMainPackage(ctx, path.Join("cmd", e.Name())) {
				mains = append(mains, e.Name())
			}
		}
		if len(mains) != 1 {
			return "", errors.New("starter:go needs a main package in the build context root or exactly one cmd/<name> main package")
		}
		pkg = "./cmd/" + mains[0]
	}
	var d dockerfile
	d.line("FROM "+starterGoImage+" AS build", version)
	d.line("WORKDIR /src")
	d.line("COPY go.mod go.sum* ./")
	d.line("RUN go mod download")
	d.line("COPY . .")
	d.line("RUN CGO_ENABLED=0 go build -trimpath -o /out/app %s", pkg)
	d.line("")
	d.line("FROM %s", starterGoRuntime)
	d.line("RUN apk add --no-cache ca-certificates tzdata")
	d.line("COPY --from=build /out/app /usr/local/bin/app")
	d.line("%s", cmdJSON("app"))
	return d.String(), nil
}

func starterRuby(ctx string) (string, error) {
	if !exists(ctx, "Gemfile") {
		return "", errors.New("starter:ruby needs a Gemfile in the build context")
	}
	var d dockerfile
	d.line("FROM %s", starterRubyImage)
	d.line("RUN apt-get update && apt-get install -y --no-install-recommends build-essential git libpq-dev libyaml-dev && rm -rf /var/lib/apt/lists/*")
	d.line("WORKDIR /app")
	d.line("ENV BUNDLE_WITHOUT=development:test")
	d.line("COPY Gemfile Gemfile.lock* ./")
	d.line("RUN bundle install")
	d.line("COPY . .")
	switch {
	case exists(ctx, "bin/rails"):
		d.line("ENV RAILS_ENV=production RAILS_LOG_TO_STDOUT=1")
		d.line("%s", cmdJSON("sh", "-c", `exec bin/rails server -b 0.0.0.0 -p "${PORT:-3000}"`))
	case exists(ctx, "config.ru"):
		d.line("%s", cmdJSON("sh", "-c", `exec bundle exec rackup --host 0.0.0.0 --port "${PORT:-3000}"`))
	}
	return d.String(), nil
}

func starterPHP(ctx string) (string, error) {
	var d dockerfile
	d.line("FROM %s", starterPHPImage)
	d.line("ENV PORT=80")
	docroot := "/var/www/html"
	if exists(ctx, "public/index.php") {
		docroot += "/public"
	}
	d.line("ENV APACHE_DOCUMENT_ROOT=%s", docroot)
	d.line("RUN a2enmod rewrite \\")
	d.line(" && sed -ri 's/Listen 80$/Listen ${PORT}/' /etc/apache2/ports.conf \\")
	d.line(" && sed -ri 's#<VirtualHost \\*:80>#<VirtualHost *:${PORT}>#' /etc/apache2/sites-available/000-default.conf \\")
	d.line(" && sed -ri 's#/var/www/html#${APACHE_DOCUMENT_ROOT}#g' /etc/apache2/sites-available/000-default.conf /etc/apache2/apache2.conf")
	if exists(ctx, "composer.json") {
		d.line("RUN apt-get update && apt-get install -y --no-install-recommends git unzip && rm -rf /var/lib/apt/lists/*")
		d.line("COPY --from=%s /usr/bin/composer /usr/bin/composer", starterComposer)
	}
	d.line("WORKDIR /var/www/html")
	d.line("COPY . .")
	if exists(ctx, "composer.json") {
		d.line("RUN composer install --no-dev --optimize-autoloader --no-interaction")
	}
	d.line("RUN chown -R www-data:www-data /var/www/html")
	return d.String(), nil
}

func starterStatic(ctx string) (string, error) {
	root := ""
	switch {
	case exists(ctx, "index.html"):
	case exists(ctx, "public/index.html"):
		root = "public"
	case exists(ctx, "dist/index.html"):
		root = "dist"
	case exists(ctx, "build/index.html"):
		root = "build"
	default:
		return "", errors.New("starter:static needs an index.html in the build context (or in public/, dist/ or build/); it serves prebuilt files and does not run a build command")
	}
	src := "."
	if root != "" {
		src = root
	}
	var d dockerfile
	d.line("FROM %s", starterNginxImage)
	d.line("ENV PORT=80")
	// nginx's entrypoint renders /etc/nginx/templates/*.template with envsubst,
	// so the listen port follows the PORT the platform sets.
	d.line(`RUN printf 'server {\n  listen ${PORT};\n  root /usr/share/nginx/html;\n  index index.html;\n  location / { try_files $uri $uri/ =404; }\n}\n' > /etc/nginx/templates/default.conf.template`)
	d.line("COPY %s /usr/share/nginx/html", src)
	return d.String(), nil
}

// StarterIgnore is the .dockerignore of a generated starter Dockerfile. It
// replaces nothing of the user's: Build appends the repository's own
// .dockerignore. .git must never reach an image (the static starter would
// publish it).
func StarterIgnore(lang string) string {
	ignore := ".git\n"
	if lang == "node" {
		ignore += "node_modules\n"
	}
	return ignore
}
