// Package envtemplate reads the template env files of a repository
// (.env.example and its siblings). The parser is shared by the agent, which
// scans a checkout, and the control plane, which reads the files through a git
// provider's API before anything is deployed.
//
// Only explicit template files are ever read, never .env, .env.local or
// .env.production: those hold real values.
package envtemplate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Files are the template env file names looked for, in this order, in the
// repository root and in the build context directory.
var Files = []string{".env.example", ".env.sample", ".env.template", ".env.dist"}

// Parse reads KEY=value lines. The comment block directly above a key becomes
// its Comment. Duplicate keys keep the first entry; at most limit entries are
// returned. Values are cut at 512 bytes and comments at 200.
func Parse(r io.Reader, limit int) []agentprotocol.EnvVarHint {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), agentprotocol.ScanMaxFileBytes)
	var vars []agentprotocol.EnvVarHint
	seen := map[string]bool{}
	var comment []string
	for sc.Scan() && len(vars) < limit {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			comment = comment[:0]
			continue
		case strings.HasPrefix(line, "#"):
			comment = append(comment, strings.TrimSpace(strings.TrimPrefix(line, "#")))
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !ValidKey(key) || seen[key] {
			comment = comment[:0]
			continue
		}
		seen[key] = true
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && strings.IndexByte(val[1:], val[0]) >= 0 {
			// Quoted: up to the closing quote; anything after it (a comment) is dropped.
			val = val[1 : 1+strings.IndexByte(val[1:], val[0])]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if len(val) > 512 {
			val = val[:512]
		}
		c := strings.Join(comment, " ")
		if len(c) > 200 {
			c = c[:200]
		}
		vars = append(vars, agentprotocol.EnvVarHint{Key: key, Value: val, Comment: c})
		comment = comment[:0]
	}
	return vars
}

// ValidKey reports whether k is a legal variable name.
func ValidKey(k string) bool {
	if k == "" || len(k) > 255 {
		return false
	}
	for i := range len(k) {
		c := k[i]
		switch {
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// ReadFunc returns the content of the repository file at a slash-separated
// path relative to the repository root. found is false, with a nil error, when
// the file does not exist.
type ReadFunc func(path string) (data []byte, found bool, err error)

// ErrBadContext is returned for a build context that leaves the repository.
var ErrBadContext = errors.New("the build context must be a relative path inside the repository")

// CleanContext normalises a build context directory: "" and "." become "" (the
// repository root); anything absolute or containing a ".." segment is rejected.
func CleanContext(dir string) (string, error) {
	dir = strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	if dir == "" || dir == "." || dir == "./" {
		return "", nil
	}
	if strings.HasPrefix(dir, "/") || strings.Contains(dir, "\x00") {
		return "", ErrBadContext
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == ".." {
			return "", ErrBadContext
		}
	}
	dir = path.Clean(dir)
	if dir == "." {
		return "", nil
	}
	return dir, nil
}

// Scan reads Files in the repository root and, when different, in contextDir.
// Files over agentprotocol.ScanMaxFileBytes and files without a variable are
// skipped; at most agentprotocol.ScanMaxVars variables are returned in total.
// The first read error stops the scan. The result is nil when nothing was
// found.
func Scan(contextDir string, read ReadFunc) ([]agentprotocol.EnvFileScan, error) {
	ctxDir, err := CleanContext(contextDir)
	if err != nil {
		return nil, err
	}
	dirs := []string{""}
	if ctxDir != "" {
		dirs = append(dirs, ctxDir)
	}
	var out []agentprotocol.EnvFileScan
	budget := agentprotocol.ScanMaxVars
	for _, dir := range dirs {
		for _, name := range Files {
			if budget <= 0 {
				return out, nil
			}
			p := name
			if dir != "" {
				p = dir + "/" + name
			}
			data, found, err := read(p)
			if err != nil {
				return out, fmt.Errorf("read %s: %w", p, err)
			}
			if !found || len(data) > agentprotocol.ScanMaxFileBytes {
				continue
			}
			vars := Parse(strings.NewReader(string(data)), budget)
			if len(vars) == 0 {
				continue
			}
			budget -= len(vars)
			out = append(out, agentprotocol.EnvFileScan{Path: p, Vars: vars})
		}
	}
	return out, nil
}
