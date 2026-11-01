package build

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxRepoFileRead bounds a repository file the agent reads to decide how to
// build it (package.json, .nvmrc, .dockerignore, Go sources, ...).
const maxRepoFileRead = 1 << 20

var errNotRegularFile = errors.New("not a regular file")

// readRepoFile reads rel (a slash-separated path below root) from an untrusted
// checkout. A repository can contain symlinks to anywhere (package.json ->
// /dev/zero, .nvmrc -> ../../../etc/shadow): the lookup is confined to root
// (a link that leaves it is refused, one that stays inside is followed), the
// target must be a regular file and at most maxRepoFileRead bytes are read.
func readRepoFile(root, rel string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	rel = filepath.FromSlash(rel)
	// Check before opening: opening a FIFO or device would block or never end.
	if st, err := r.Stat(rel); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", rel, errNotRegularFile)
	}
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", rel, errNotRegularFile)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRepoFileRead+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRepoFileRead {
		return nil, fmt.Errorf("%s is larger than %d KiB", rel, maxRepoFileRead>>10)
	}
	return data, nil
}

// readRepoDir lists dir (below root) like os.ReadDir, confined to root.
func readRepoDir(root, rel string) ([]os.DirEntry, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := r.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}

// redactGitURL removes the userinfo of an http(s):// or git:// URL for logs;
// other forms (scp-like, ssh://) are returned as they are.
func redactGitURL(raw string) string {
	if !strings.Contains(raw, "://") || strings.HasPrefix(strings.ToLower(raw), "ssh://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return urlUserinfoRe.ReplaceAllString(raw, "${1}")
	}
	if u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

var urlUserinfoRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://)[^/]*@`)
