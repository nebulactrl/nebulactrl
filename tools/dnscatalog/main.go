// Command dnscatalog generates pkg/agentprotocol/dns_lego_catalog.go: the DNS-01
// providers of the lego release that the pinned Traefik vendors, with the
// environment variables each one reads.
//
//	go run ./tools/dnscatalog -traefik v3.7.16 -o pkg/agentprotocol/dns_lego_catalog.go
//
// It reads Traefik's go.mod at the tag for the github.com/go-acme/lego/v5
// version, downloads that lego tag from GitHub and parses
// providers/dns/<code>/<code>.toml, the file lego generates its documentation
// from (https://go-acme.github.io/lego/dns/<code>/). Run it whenever
// proxy.TraefikImage changes; TestCatalogMatchesPinnedTraefik fails until you do.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// excluded are lego providers that never become a credential: exec runs a
// program, manual waits on stdin. Neither makes sense inside the proxy container.
var excluded = map[string]bool{"exec": true, "manual": true}

var (
	envName   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	legoRe    = regexp.MustCompile(`(?m)^\s*github\.com/go-acme/lego/v5\s+(v\S+)`)
	fileLike  = regexp.MustCompile(`(_FILE|_PATH|_PATHS|_FILE_PATH|_KEYFILE)$`)
	headerRe  = regexp.MustCompile(`^\[([A-Za-z.]+)\]\s*(#.*)?$`)
	topKeyRe  = regexp.MustCompile(`^(Name|URL|Code)\s*=\s*(.*)$`)
	aliasesRe = regexp.MustCompile(`^Aliases\s*=\s*\[(.*)\]`)
	entryRe   = regexp.MustCompile(`^\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z0-9_]+))\s*=\s*(.*)$`)
)

type env struct {
	Name, Desc string
	Optional   bool
}

type provider struct {
	Code, Name, URL string
	Aliases         []string
	Env             []env
}

func main() {
	traefik := flag.String("traefik", "", "Traefik tag whose lego version is catalogued, e.g. v3.7.16")
	out := flag.String("o", "", "output file (default stdout)")
	flag.Parse()
	if *traefik == "" {
		fatal("-traefik is required")
	}
	lego, err := legoVersion(*traefik)
	check(err)
	providers, err := fetchProviders(lego)
	check(err)
	src, err := render(*traefik, lego, providers)
	check(err)
	if *out == "" {
		_, _ = os.Stdout.Write(src)
		return
	}
	check(os.WriteFile(*out, src, 0o644))
	fmt.Fprintf(os.Stderr, "wrote %s: %d providers from lego %s (Traefik %s)\n", *out, len(providers), lego, *traefik)
}

func check(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "dnscatalog:", msg)
	os.Exit(1)
}

func get(url string) (io.ReadCloser, error) {
	c := &http.Client{Timeout: 2 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func legoVersion(traefik string) (string, error) {
	body, err := get("https://raw.githubusercontent.com/traefik/traefik/" + traefik + "/go.mod")
	if err != nil {
		return "", err
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	m := legoRe.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("traefik %s go.mod does not require github.com/go-acme/lego/v5", traefik)
	}
	return string(m[1]), nil
}

func fetchProviders(lego string) ([]provider, error) {
	body, err := get("https://codeload.github.com/go-acme/lego/tar.gz/refs/tags/" + lego)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var out []provider
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// lego-<version>/providers/dns/<code>/<code>.toml
		parts := strings.Split(h.Name, "/")
		if len(parts) != 5 || parts[1] != "providers" || parts[2] != "dns" || path.Ext(parts[4]) != ".toml" || parts[4] != parts[3]+".toml" {
			continue
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		p, err := parseTOML(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", h.Name, err)
		}
		if p.Code != parts[3] {
			return nil, fmt.Errorf("%s: Code = %q", h.Name, p.Code)
		}
		if excluded[p.Code] {
			continue
		}
		out = append(out, p)
	}
	if len(out) < 100 {
		return nil, fmt.Errorf("only %d providers found in lego %s", len(out), lego)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, " #"); i > 0 && !strings.ContainsAny(v[i:], `"'`) {
		v = strings.TrimSpace(v[:i])
	}
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return strings.Join(strings.Fields(v), " ")
}

// parseTOML reads the few keys it needs from a lego provider file: the
// top-level Name, URL and Code and the entries of [Configuration.Credentials]
// and [Configuration.Additional]. Multi-line strings (Description, Example,
// Additional) are skipped.
func parseTOML(raw []byte) (provider, error) {
	var p provider
	section := ""
	multi := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if multi != "" {
			if strings.Contains(line, multi) {
				multi = ""
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := headerRe.FindStringSubmatch(trimmed); m != nil {
			section = m[1]
			continue
		}
		// A multi-line string opens on this line and is not closed on it.
		for _, q := range []string{`'''`, `"""`} {
			if n := strings.Count(line, q); n%2 == 1 {
				multi = q
			}
		}
		if multi != "" {
			continue
		}
		switch section {
		case "":
			if m := aliasesRe.FindStringSubmatch(trimmed); m != nil {
				for _, a := range strings.Split(m[1], ",") {
					if a = unquote(a); a != "" {
						p.Aliases = append(p.Aliases, a)
					}
				}
				continue
			}
			if m := topKeyRe.FindStringSubmatch(trimmed); m != nil {
				v := unquote(m[2])
				switch m[1] {
				case "Name":
					p.Name = v
				case "URL":
					p.URL = v
				case "Code":
					p.Code = v
				}
			}
		case "Configuration.Credentials", "Configuration.Additional":
			m := entryRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[1] + m[2] + m[3]
			if !envName.MatchString(name) || fileLike.MatchString(name) {
				continue
			}
			p.Env = append(p.Env, env{Name: name, Desc: describe(unquote(m[4])), Optional: section == "Configuration.Additional"})
		}
	}
	if p.Code == "" || p.Name == "" {
		return p, fmt.Errorf("missing Name or Code")
	}
	return p, nil
}

var mdLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

// describe trims a lego description to a short label: links become their
// text, backticks go, and it ends at 120 characters.
func describe(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "`", "")
	if len(s) > 120 {
		s = strings.TrimSpace(s[:117]) + "..."
	}
	return s
}

func render(traefik, lego string, providers []provider) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, `// Code generated by tools/dnscatalog; DO NOT EDIT.

package agentprotocol

// LegoCatalogTraefik is the Traefik release whose lego the catalog below was
// generated for. It must equal the tag of the pinned proxy image
// (TestCatalogMatchesPinnedTraefik).
const LegoCatalogTraefik = %q

// LegoVersion is the github.com/go-acme/lego/v5 version that Traefik
// LegoCatalogTraefik vendors (its go.mod). Environment variable names are those
// of providers/dns/<code>/<code>.toml at this tag.
const LegoVersion = %q

// legoCatalog lists every DNS provider of LegoVersion that can run inside the
// proxy container, sorted by code. Credentials come first in each Env list,
// then the optional tuning variables. Variables that read a file (names ending
// in _FILE or _PATH) are left out: the container has no mounts to read.
var legoCatalog = []legoProvider{
`, traefik, lego)
	for _, p := range providers {
		aliases := ""
		if len(p.Aliases) > 0 {
			aliases = fmt.Sprintf(" Aliases: %#v,", p.Aliases)
		}
		fmt.Fprintf(&b, "\t{Code: %q, Name: %q, URL: %q,%s Env: []legoEnv{\n", p.Code, p.Name, p.URL, aliases)
		for _, e := range p.Env {
			fmt.Fprintf(&b, "\t\t{Name: %q, Desc: %q, Optional: %t},\n", e.Name, e.Desc, e.Optional)
		}
		b.WriteString("\t}},\n")
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}
