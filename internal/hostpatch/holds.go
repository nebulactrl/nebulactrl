package hostpatch

import (
	"fmt"
	"regexp"
	"strings"
)

// BuiltinHolds are the packages a scheduled upgrade never touches: Docker's and
// containerd's packages restart the daemon (and with it every container that has
// no live-restore) when they are upgraded, so they stay in the manual Apply flow
// where a person sees the plan. Names are matched exactly; a trailing "*" is a
// prefix (moby-*).
var BuiltinHolds = []string{
	"docker-ce", "docker-ce-cli", "docker-ce-rootless-extras", "docker-buildx-plugin", "docker-compose-plugin",
	"containerd.io", "docker.io", "containerd", "runc", "moby-*",
}

// MaxPackageNameLen bounds a package name.
const MaxPackageNameLen = 100

var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)

// ValidPackageName reports whether name is a plausible package name for apt or
// rpm: letters, digits and "+._-", starting with a letter or digit. It is what
// a hold may contain, and keeps a hold from carrying shell syntax.
func ValidPackageName(name string) bool {
	return len(name) <= MaxPackageNameLen && packageNamePattern.MatchString(name)
}

// holdPatterns is the built-in and extra holds as one POSIX shell case pattern
// list ("docker-ce|runc|moby-*|vim"). Extra holds are validated first, so none
// can carry shell syntax.
func holdPatterns(extra []string) (string, error) {
	all := append([]string{}, BuiltinHolds...)
	for _, h := range extra {
		if !ValidPackageName(h) {
			return "", fmt.Errorf("invalid package name %q", h)
		}
		all = append(all, h)
	}
	return strings.Join(all, "|"), nil
}

// excludeList is the holds as the comma-separated value of dnf's --exclude.
func excludeList(extra []string) (string, error) {
	p, err := holdPatterns(extra)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(p, "|", ","), nil
}
