// Package version parses and compares NebulaCtrl release versions and holds the
// version of the running control plane. The control plane, the CLI and the agent
// are built from one tag, so "the agent is behind" is a comparison of two of
// these strings.
package version

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// Version is a parsed semantic version (https://semver.org, precedence rules
// of section 11). Build metadata is dropped: it never affects precedence.
type Version struct {
	Major, Minor, Patch uint64
	// Pre holds the dot-separated pre-release identifiers ("rc", "1").
	Pre []string
}

// Parse reads "1.4.0", "v1.4.0", "1.4.0-rc.1" or "1.4.0+build". It rejects
// anything else, including "dev" and git-describe output such as
// "v1.4.0-3-gabc1234-dirty" (that is a pre-release by the grammar, but it means
// "after 1.4.0", so treating it as a version would misorder it).
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return Version{}, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
		if pre == "" || isDescribe(pre) {
			return Version{}, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]uint64
	for i, p := range parts {
		v, ok := parseNum(p)
		if !ok {
			return Version{}, false
		}
		n[i] = v
	}
	v := Version{Major: n[0], Minor: n[1], Patch: n[2]}
	if pre != "" {
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" || !validIdent(id) {
				return Version{}, false
			}
			if isNumeric(id) && len(id) > 1 && id[0] == '0' {
				return Version{}, false
			}
		}
	}
	return v, true
}

// isDescribe recognizes the "<commits>-g<sha>[-dirty]" tail of git describe.
func isDescribe(pre string) bool {
	parts := strings.Split(pre, "-")
	if len(parts) < 2 {
		return false
	}
	return isNumeric(parts[0]) && len(parts[1]) > 1 && parts[1][0] == 'g'
}

func parseNum(p string) (uint64, bool) {
	if p == "" || (len(p) > 1 && p[0] == '0') || !isNumeric(p) {
		return 0, false
	}
	v, err := strconv.ParseUint(p, 10, 64)
	return v, err == nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validIdent(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
			return false
		}
	}
	return true
}

// Compare returns -1, 0 or 1 by semantic version precedence.
func Compare(a, b Version) int {
	for _, d := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1 // a release is newer than its pre-releases
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := comparePre(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

func comparePre(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case an:
		return -1 // numeric identifiers sort before alphanumeric ones
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// Skew describes how an agent's version relates to the control plane's.
type Skew string

const (
	// SkewCurrent: the versions are equal.
	SkewCurrent Skew = "current"
	// SkewBehind: the agent is older than the control plane.
	SkewBehind Skew = "behind"
	// SkewAhead: the agent is newer than the control plane. Never upgraded; the
	// control plane must be upgraded first (docs/installing/upgrade.md).
	SkewAhead Skew = "ahead"
	// SkewUnknown: a version is missing or not a release version (a development
	// build), so no order can be told.
	SkewUnknown Skew = "unknown"
)

// Compute returns the skew of agent against control. Equal strings are
// current, whatever they are, so a development build matches itself.
func Compute(agent, control string) Skew {
	if agent != "" && agent == control {
		return SkewCurrent
	}
	a, aok := Parse(agent)
	c, cok := Parse(control)
	if !aok || !cok {
		return SkewUnknown
	}
	switch Compare(a, c) {
	case -1:
		return SkewBehind
	case 1:
		return SkewAhead
	}
	return SkewCurrent
}

// Upgradable reports whether an agent in this skew may be upgraded by an
// administrator: it is behind, or its order is unknown but the versions differ
// (for example a development build). A newer agent is never downgraded.
func (s Skew) Upgradable() bool { return s == SkewBehind || s == SkewUnknown }

// AutoUpgradable reports whether the automatic upgrade may pick this skew: only
// a provably older agent. An unknown order is left to the administrator.
func (s Skew) AutoUpgradable() bool { return s == SkewBehind }

var current atomic.Value // string

// SetCurrent records the version of the running control plane (set once at
// startup from the -ldflags value).
func SetCurrent(v string) { current.Store(strings.TrimSpace(v)) }

// Current returns the control plane's version ("dev" when never set).
func Current() string {
	if v, _ := current.Load().(string); v != "" {
		return v
	}
	return "dev"
}
