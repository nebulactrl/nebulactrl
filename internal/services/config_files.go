package services

import (
	"path"
	"regexp"
	"strings"
)

// File mount limits.
const (
	MaxFiles         = 16
	MaxFileBytes     = 64 << 10
	MaxFilesTotalLen = 256 << 10
)

var fileModeRe = regexp.MustCompile(`^0?[0-7]{3,4}$`)

// FileMount is a file written into the container before it first starts. The
// content is stored in the (plain) service config: never put secrets in it.
type FileMount struct {
	Path    string `json:"path"`
	Mode    string `json:"mode,omitempty"` // octal, default 0644
	Content string `json:"content"`
}

// validateTemplateFields checks the file mounts and one-shot settings.
func (c Config) validateTemplateFields() error {
	if len(c.Files) > MaxFiles {
		return invalid("at most %d files", MaxFiles)
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range c.Files {
		if !path.IsAbs(f.Path) || strings.Contains(f.Path, "..") || path.Clean(f.Path) != f.Path || f.Path == "/" {
			return invalid("file path %q must be a clean absolute path without ..", f.Path)
		}
		if seen[f.Path] {
			return invalid("duplicate file %q", f.Path)
		}
		seen[f.Path] = true
		if f.Mode != "" && !fileModeRe.MatchString(f.Mode) {
			return invalid("file %s: mode %q must be octal like 0644", f.Path, f.Mode)
		}
		if len(f.Content) > MaxFileBytes {
			return invalid("file %s exceeds %d bytes", f.Path, MaxFileBytes)
		}
		total += len(f.Content)
	}
	if total > MaxFilesTotalLen {
		return invalid("files exceed %d bytes in total", MaxFilesTotalLen)
	}
	if c.OneShot && c.Port != 0 {
		return invalid("a one-shot service has no port")
	}
	return nil
}
