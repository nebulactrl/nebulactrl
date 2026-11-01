// Package installdeploy embeds the installer (install.sh, uninstall.sh, the
// control-plane compose file and the shell libraries) and assembles the
// self-contained script the control plane serves at /install.sh.
package installdeploy

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed install.sh uninstall.sh docker-compose.yml lib instance
var files embed.FS

// Markers in install.sh the assembly replaces.
const (
	assetsMarker  = "#@assets"
	includePrefix = "#@include "
)

// Uninstall returns uninstall.sh.
func Uninstall() string { return mustRead("uninstall.sh") }

// Compose returns the installed control plane's docker-compose.yml.
func Compose() string { return mustRead("docker-compose.yml") }

// InstanceConfig returns one of InstanceConfigs by name ("instance/registry.yaml").
func InstanceConfig(name string) string { return mustRead(name) }

// InstanceConfigs lists the built-in services' configuration files the installer
// writes to <dir>/instance/ (the compose file mounts them read-only).
func InstanceConfigs() []string {
	return []string{"instance/master.toml", "instance/registry.yaml", "instance/loki.yaml"}
}

// Script returns install.sh with everything it needs inlined: the files it
// installs (nc_asset) and the libraries at their #@include markers. The result
// runs with `curl ... | bash` and needs no other download from this repository.
func Script() string {
	src := mustRead("install.sh")
	var out strings.Builder
	for _, line := range strings.SplitAfter(src, "\n") {
		trimmed := strings.TrimRight(line, "\n")
		switch {
		case trimmed == assetsMarker:
			out.WriteString(assetFunction())
		case strings.HasPrefix(trimmed, includePrefix):
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, includePrefix))
			body, err := files.ReadFile(name)
			if err != nil {
				// A library that does not exist is loaded (or skipped) at run time.
				out.WriteString(line)
				continue
			}
			out.WriteString(stripShebang(string(body)))
			if !strings.HasSuffix(out.String(), "\n") {
				out.WriteString("\n")
			}
		default:
			out.WriteString(line)
		}
	}
	return out.String()
}

// assetFunction redefines nc_asset with the installed files inlined as here-documents.
func assetFunction() string {
	assets := append([]string{"docker-compose.yml", "uninstall.sh"}, InstanceConfigs()...)
	var b strings.Builder
	b.WriteString("nc_asset() {\n\tcase \"$1\" in\n")
	for i, name := range assets {
		delim := fmt.Sprintf("NC_ASSET_EOF_%d", i)
		body := mustRead(name)
		if strings.Contains(body, delim) {
			panic("installdeploy: asset " + name + " contains its own here-document delimiter")
		}
		fmt.Fprintf(&b, "\t%s)\n\t\tcat <<'%s'\n%s", name, delim, body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n\t\t;;\n", delim)
	}
	b.WriteString("\t*) return 1 ;;\n\tesac\n}\n")
	return b.String()
}

func stripShebang(s string) string {
	if strings.HasPrefix(s, "#!") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[i+1:]
		}
		return ""
	}
	return s
}

func mustRead(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic("installdeploy: " + err.Error())
	}
	return string(b)
}

// Libraries lists the shell libraries that ship with the installer.
func Libraries() []string {
	var out []string
	_ = fs.WalkDir(files, "lib", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".sh") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}
