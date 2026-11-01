package gitproviders

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// MaxWatchPaths bounds the watch patterns of one service.
const MaxWatchPaths = 50

// ValidateWatchPaths normalizes and checks monorepo watch patterns: gitignore
// style globs relative to the repository root (`apps/api/**`, `packages/*/src`,
// `Dockerfile`), where `**` crosses directories, a trailing `/` or a plain
// directory name watches everything below it and a leading `!` excludes.
func ValidateWatchPaths(patterns []string) ([]string, error) {
	if len(patterns) > MaxWatchPaths {
		return nil, fmt.Errorf("at most %d watch paths are allowed", MaxWatchPaths)
	}
	out := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if len(p) > 255 || strings.ContainsAny(p, "\x00\n\r") {
			return nil, fmt.Errorf("watch path %q is too long or contains control characters", truncate(p, 40))
		}
		body := strings.TrimPrefix(p, "!")
		for _, seg := range splitPath(body) {
			if seg == ".." {
				return nil, fmt.Errorf("watch path %q must not contain ..", p)
			}
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, ""); errors.Is(err, path.ErrBadPattern) {
				return nil, fmt.Errorf("watch path %q is not a valid pattern", p)
			}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func splitPath(p string) []string {
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}
	return segs
}

// MatchWatchPaths reports whether a push touching changed should deploy a
// service with the given watch patterns: some changed file matches an include
// pattern (or there is none) and is not excluded. No patterns means every push.
func MatchWatchPaths(patterns, changed []string) bool {
	var inc, exc [][]string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		segs := splitPath(p)
		if len(segs) == 0 {
			continue
		}
		if neg {
			exc = append(exc, segs)
		} else {
			inc = append(inc, segs)
		}
	}
	if len(inc) == 0 && len(exc) == 0 {
		return true
	}
	for _, c := range changed {
		name := splitPath(c)
		if len(name) == 0 {
			continue
		}
		included := len(inc) == 0
		for _, p := range inc {
			if matchSegs(p, name) {
				included = true
				break
			}
		}
		if !included {
			continue
		}
		excluded := false
		for _, p := range exc {
			if matchSegs(p, name) {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

// matchSegs matches a path against a pattern, where a pattern also covers
// everything below the paths it matches (`apps/api` watches `apps/api/x.go`).
func matchSegs(pat, name []string) bool {
	if len(pat) == 0 {
		return true // the pattern is used up: the name is below the matched directory
	}
	if len(name) == 0 {
		return len(pat) == 1 && pat[0] == "**"
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegs(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
		return false
	}
	return matchSegs(pat[1:], name[1:])
}
