package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// defaultSchemaOrigin hosts the published schema when the CLI is not logged in.
const defaultSchemaOrigin = "https://nebulactrl.com"

func newConfigInitCmd(c *cli) *cobra.Command {
	var file, project, env, name, image, gitURL string
	var force bool
	cmd := &cobra.Command{
		Use: "init", Short: "Write a starter nebula.toml for this repository", Args: noArgs,
		Long: "Looks at the directory (Dockerfile, compose file, package.json, go.mod, pyproject.toml, Gemfile, composer.json, .env.example, the git remote) and writes a commented nebula.toml with one service. It works offline and never overwrites a file without --force.\n" +
			"With --project and --env it does not guess: it exports the live environment instead, like `config export`. Review the file, then run `config validate`, `config plan` and `config apply`.",
		Example: "nebulactrl config init\nnebulactrl config init --image ghcr.io/acme/api:1.4 --name api\nnebulactrl config init --project acme-shop --env production",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := file
			if out == "" {
				out = configFileName
			}
			if env != "" && project == "" {
				return usagef("--env needs --project: with both, init exports the live environment")
			}
			exporting := project != "" && env != ""
			if exporting && (image != "" || gitURL != "" || name != "") {
				return usagef("--image, --git and --name scaffold a new file; with --project and --env init exports the live environment")
			}
			if image != "" && gitURL != "" {
				return usagef("use either --image or --git, not both")
			}
			if image != "" && (strings.ContainsFunc(image, func(r rune) bool { return r <= ' ' || r == 0x7f })) {
				return usagef("--image %q must not contain whitespace", image)
			}
			if gitURL != "" && cleanGitURL(gitURL) != gitURL {
				return usagef("--git must be a plain repository URL without credentials (https://host/org/repo.git or git@host:org/repo.git)")
			}
			if _, err := os.Lstat(out); err == nil && !force {
				return &exitError{code: 1, err: fmt.Errorf("%s already exists; pass --force to overwrite it", out)}
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if exporting {
				return c.runExport(cmd.Context(), project, "", env, "", out, false, false)
			}
			abs, err := filepath.Abs(out)
			if err != nil {
				return err
			}
			scan := scanRepo(filepath.Dir(abs))
			text, summary, err := scaffoldConfig(scan, scaffoldOptions{
				origin: c.schemaOrigin(), project: project, name: name, image: image, gitURL: gitURL,
			})
			if err != nil {
				return err
			}
			if err := writeNewFile(out, []byte(text), force); err != nil {
				return err
			}
			_, _, ds := projectconfig.Parse([]byte(text), nil)
			if c.asJSON {
				return c.printJSON(map[string]any{"file": out, "summary": summary, "diagnostics": nonNilDiagnostics(ds)})
			}
			c.info("Wrote %s (%s)", out, summary)
			if ds.HasErrors() {
				c.note("The file is not complete yet:")
				writeDiagnostics(c.errOut, displayPath(out), "  ", ds)
			}
			c.note("Next: check it with `nebulactrl config validate --offline`, then `config plan --env <environment>` and `config apply`.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "where to write the file (default: nebula.toml in the current directory)")
	f.BoolVar(&force, "force", false, "overwrite an existing file")
	f.StringVar(&name, "name", "", "display name of the service (its slug is derived from it, default: the directory name)")
	f.StringVar(&image, "image", "", "scaffold an image service for this image instead of looking at the repository")
	f.StringVar(&gitURL, "git", "", "repository URL of the git service (default: the origin remote of this repository)")
	f.StringVar(&project, "project", "", "project slug to write into the file; with --env, export that project's live environment instead")
	f.StringVar(&env, "env", "", "with --project: the environment to export")
	return cmd
}

func nonNilDiagnostics(ds projectconfig.Diagnostics) projectconfig.Diagnostics {
	if ds == nil {
		return projectconfig.Diagnostics{}
	}
	return ds
}

// schemaOrigin is the control plane the `#:schema` line points at: the one the
// CLI is logged in to, else the published site.
func (c *cli) schemaOrigin() string {
	cfg := c.cfg
	if cfg.URL == "" {
		if loaded, err := loadConfig(); err == nil {
			cfg = loaded
		}
	}
	if cfg.URL == "" {
		return defaultSchemaOrigin
	}
	return controlPlaneOrigin(cfg.URL)
}

// writeNewFile writes data to path, failing when it exists unless force.
func writeNewFile(path string, data []byte, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return &exitError{code: 1, err: fmt.Errorf("%s already exists; pass --force to overwrite it", path)}
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

// ---- repository detection ---------------------------------------------------

// repoScan is what init found in a directory.
type repoScan struct {
	dir        string
	dockerfile bool
	expose     int    // first EXPOSE port of the Dockerfile, 0 when none
	compose    string // compose file name, "" when none
	lang       string // node, go, python, ruby, php, static; "" when none
	envNames   []string
	remote     string // origin URL without credentials, "" when none
	branch     string
}

var (
	exposeRe  = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(\d{1,5})`)
	envNameRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	scpURLRe  = regexp.MustCompile(`^git@[A-Za-z0-9.-]+:[A-Za-z0-9._~][A-Za-z0-9._~/-]*$`)
)

// scanRepo looks at dir only; it reads a few small files and runs nothing.
func scanRepo(dir string) repoScan {
	s := repoScan{dir: dir}
	at := func(name string) string { return filepath.Join(dir, name) }
	if fileExists(at("Dockerfile")) {
		s.dockerfile = true
		s.expose = dockerfileExpose(at("Dockerfile"))
	}
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if fileExists(at(n)) {
			s.compose = n
			break
		}
	}
	for _, l := range []struct{ lang, file string }{
		{"node", "package.json"}, {"go", "go.mod"}, {"python", "pyproject.toml"}, {"python", "requirements.txt"},
		{"python", "Pipfile"}, {"ruby", "Gemfile"}, {"php", "composer.json"}, {"static", "index.html"},
	} {
		if fileExists(at(l.file)) {
			s.lang = l.lang
			break
		}
	}
	s.envNames = envExampleNames(at(".env.example"))
	s.remote, s.branch = gitOrigin(dir)
	return s
}

func dockerfileExpose(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if m := exposeRe.FindStringSubmatch(sc.Text()); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n <= 65535 {
				return n
			}
		}
	}
	return 0
}

// envExampleNames returns the variable names of a .env.example (never values).
func envExampleNames(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var names []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && len(names) < 50 {
		if m := envNameRe.FindStringSubmatch(sc.Text()); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// gitOrigin reads the origin remote URL and the checked-out branch of the
// repository containing dir, without running git. The URL is returned only
// when it is a plain remote URL (credentials are dropped).
func gitOrigin(dir string) (remote, branch string) {
	root := gitRoot(dir)
	if root == "" {
		return "", ""
	}
	gitDir := filepath.Join(root, ".git")
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return "", ""
	}
	if raw, err := readLimited(filepath.Join(gitDir, "config"), 1<<20); err == nil {
		inOrigin := false
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "["):
				inOrigin = line == `[remote "origin"]`
			case inOrigin && strings.HasPrefix(line, "url"):
				if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "url" {
					remote = cleanGitURL(strings.TrimSpace(v))
				}
			}
		}
	}
	if raw, err := readLimited(filepath.Join(gitDir, "HEAD"), 4096); err == nil {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "ref: refs/heads/"); ok {
			branch = ref
		}
	}
	return remote, branch
}

// cleanGitURL returns raw when it is a repository URL the file format accepts
// (http, https, git, ssh or git@host:path, no credentials), a copy without
// credentials when only the userinfo is the problem, and "" otherwise.
func cleanGitURL(raw string) string {
	if raw == "" || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if scpURLRe.MatchString(raw) {
			return raw
		}
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "http", "https", "git", "ssh":
	default:
		return ""
	}
	if u.User != nil {
		_, hasPassword := u.User.Password()
		if u.Scheme == "ssh" && u.User.Username() == "git" && !hasPassword {
			return u.String()
		}
		u.User = nil
	}
	return u.String()
}

// repoFullName guesses owner/name of a repository on a well-known host.
func repoFullName(raw string) string {
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if h, p, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":"); ok {
		host, path = h, p
	}
	switch host {
	case "github.com", "gitlab.com", "bitbucket.org":
	default:
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if parts := strings.Split(path, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return path
	}
	return ""
}

// ---- the starter file -------------------------------------------------------

var reservedServiceSlugs = map[string]bool{"shared": true, "team": true, "vault": true, "doppler": true}

// slugify makes a service slug (^[a-z0-9][a-z0-9-]{0,62}$, not reserved) from
// a name; "app" when nothing is left.
func slugify(s string) string {
	var b strings.Builder
	dash := true // no leading dash
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	if slug == "" {
		return "app"
	}
	if reservedServiceSlugs[slug] {
		return slug + "-app"
	}
	return slug
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

type scaffoldOptions struct {
	origin  string
	project string
	name    string
	image   string
	gitURL  string
}

// scaffoldConfig renders the starter file for what was found (or asked for)
// and a one-line summary. It fails when there is nothing to build a service
// from.
func scaffoldConfig(scan repoScan, o scaffoldOptions) (text, summary string, err error) {
	slug := slugify(o.name)
	if o.name == "" {
		slug = slugify(filepath.Base(scan.dir))
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("#:schema %s/schema/nebula.toml.json", strings.TrimRight(o.origin, "/"))
	w("# nebula.toml describes one project. Check it with `nebulactrl config validate`,")
	w("# preview it with `config plan --env <environment>`, stage it with `config apply`.")
	w("# Secret values never go in this file: list their names under secrets.keys.")
	w("schema = 1")
	if o.project != "" {
		w("project = %s", tomlString(o.project))
	} else {
		w("# project = \"my-project\"   # slug of the target project; or pass --project to the commands")
	}
	w("# server = \"my-server\"      # default server, by name, for every service")
	w("")

	table := "services." + slug
	service := func(port int, portHint string) {
		w("[%s]", table)
		if o.name != "" {
			w("name = %s", tomlString(o.name))
		}
		switch {
		case port > 0:
			w("port = %d%s", port, portHint)
		default:
			w("# port = 3000   # the port your app listens on; leave it out for a worker")
		}
	}

	gitService := o.gitURL != "" || scan.dockerfile || (scan.compose == "" && scan.lang != "")
	switch {
	case o.image != "":
		service(0, "")
		w("image = %s", tomlString(o.image))
		summary = fmt.Sprintf("image service %q", slug)

	case gitService:
		// A git service: the repository is built on a server.
		port, hint := 0, ""
		build := "auto"
		if scan.dockerfile {
			build = "dockerfile"
			port, hint = scan.expose, "   # from EXPOSE in the Dockerfile"
		}
		service(port, hint)
		w("")
		w("[%s.git]", table)
		remote := o.gitURL
		if remote == "" {
			remote = scan.remote
		}
		repo := repoFullName(remote)
		if remote != "" {
			w("url = %s", tomlString(remote))
		} else {
			w("# url = \"https://github.com/acme/shop.git\"   # the repository to build")
		}
		if repo != "" {
			w("# To use a git connection instead of a public URL: connection = \"<connection name>\" and repo = %s", tomlString(repo))
		}
		branch := scan.branch
		if branch == "" {
			branch = "main"
		}
		w("branch = %s", tomlString(branch))
		w("")
		w("[%s.build]", table)
		w("type = %s", tomlString(build))
		switch {
		case scan.dockerfile:
			w("dockerfile = \"Dockerfile\"")
			w("context = \".\"")
			summary = fmt.Sprintf("git service %q, Dockerfile build", slug)
		case scan.lang != "":
			w("# Detected %s. Other build types: \"dockerfile\", \"railpack\" or \"starter:<node|python|go|ruby|php|static>\".", scan.lang)
			summary = fmt.Sprintf("git service %q, %s project", slug, scan.lang)
		default:
			summary = fmt.Sprintf("git service %q", slug)
		}
		if remote == "" {
			summary += "; set git.url"
		}

	case scan.compose != "":
		w("[%s]", table)
		if o.name != "" {
			w("name = %s", tomlString(o.name))
		}
		w("")
		w("[%s.stack]", table)
		w("compose_file = %s", tomlString(scan.compose))
		w("# A compose file runs as one stack service. To manage each compose service separately,")
		w("# use `nebulactrl import compose` instead.")
		summary = fmt.Sprintf("stack service %q from %s", slug, scan.compose)

	default:
		return "", "", errors.New("nothing to scaffold from: no Dockerfile, compose file or language manifest (package.json, go.mod, pyproject.toml, Gemfile, composer.json) found here; pass --image <ref> or --git <url>, or run `nebulactrl config export` for an existing project")
	}

	if len(scan.envNames) > 0 {
		w("")
		w("# Variables named in .env.example. Give each a value, a reference such as \"${{db.DATABASE_URL}}\",")
		w("# or list it under secrets.keys when it is a secret.")
		w("# [%s.variables]", table)
		for _, n := range scan.envNames {
			w("# %s = \"\"", n)
		}
	} else {
		w("")
		w("# [%s.variables]", table)
		w("# NODE_ENV = \"production\"")
	}
	w("")
	w("# [%s.secrets]", table)
	w("# keys = [\"SESSION_SECRET\"]   # names only; set the values in the dashboard or with `nebulactrl vars`")
	return b.String(), summary, nil
}
