package pages

import (
	"archive/tar"
	"bytes"
	"io"
	"path"
	"sort"
	"strings"
)

// Archive returns the tar stream that places the bundle in a (created, not
// yet started) container when extracted at "/".
func (b *Bundle) Archive() (io.Reader, error) {
	names := make([]string, 0, len(b.files))
	for n := range b.files {
		names = append(names, n)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, n := range names {
		for d := path.Dir(n); d != "." && d != "/"; d = path.Dir(d) {
			// Only the directories this package owns: the image has /etc/nginx and /usr/share.
			if strings.HasPrefix(d+"/", rootRel+"/") || d == rootRel {
				dirs[d] = true
			}
		}
	}
	dl := make([]string, 0, len(dirs))
	for d := range dirs {
		dl = append(dl, d)
	}
	sort.Strings(dl)
	for _, d := range dl {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755}); err != nil {
			return nil, err
		}
	}
	for _, n := range names {
		c := b.files[n]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: n, Mode: 0o644, Size: int64(len(c))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(c); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}
