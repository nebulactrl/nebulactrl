package volbackup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// FileError is a failed file operation with an agentprotocol.FSErr* code.
type FileError struct {
	Code string
	Msg  string
}

func (e *FileError) Error() string { return e.Msg }

func fileErr(code, format string, a ...any) *FileError {
	return &FileError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// fileHelper starts a read-only helper for a file request.
func (r *Runner) fileHelper(ctx context.Context, req agentprotocol.VolFileRequest, logf func(string)) (*helper, func(), error) {
	if req.RequestID == "" || req.ServiceID == "" || !isSnapshotID(req.SnapshotID) || !volumeNameRe.MatchString(req.Volume) {
		return nil, nil, fileErr(agentprotocol.FSErrFailed, "invalid file request")
	}
	spec, mounts, network, err := r.destination(ctx, req.Repo)
	if err != nil {
		return nil, nil, fileErr(agentprotocol.FSErrFailed, "%v", err)
	}
	untrack := r.track(req.RequestID)
	h, err := r.newHelper(ctx, helperOpts{
		op: req.RequestID, kind: "l", serviceID: req.ServiceID, mounts: mounts, network: network, repo: spec, secrets: req.Secrets,
	}, logf)
	if err != nil {
		untrack()
		return nil, nil, fileErr(agentprotocol.FSErrFailed, "%v", err)
	}
	return h, untrack, nil
}

// lsCollector parses `restic ls --json` lines as they arrive and stops
// collecting at max nodes.
type lsCollector struct {
	partial []byte
	self    string
	max     int
	nodes   []lsNode
	// Truncated is set once more than max nodes arrived; Stop is then called.
	Truncated bool
	Stop      func()
}

func (c *lsCollector) Write(p []byte) (int, error) {
	c.partial = append(c.partial, p...)
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			break
		}
		c.line(c.partial[:i])
		c.partial = c.partial[i+1:]
	}
	if len(c.partial) > 4<<20 {
		c.partial = nil
	}
	return len(p), nil
}

func (c *lsCollector) line(line []byte) {
	if c.Truncated {
		return
	}
	for _, n := range parseLs(line, c.self) {
		if len(c.nodes) >= c.max {
			c.Truncated = true
			if c.Stop != nil {
				c.Stop()
			}
			return
		}
		c.nodes = append(c.nodes, n)
	}
}

func entryOf(n lsNode) agentprotocol.FSEntry {
	e := agentprotocol.FSEntry{Name: n.Name, Size: n.Size, Mode: fmt.Sprintf("%04o", n.Mode&0o777), LinkTarget: n.Link}
	switch n.Type {
	case "file":
		e.Type = agentprotocol.FSTypeFile
	case "dir":
		e.Type, e.Size = agentprotocol.FSTypeDir, 0
	case "symlink":
		e.Type = agentprotocol.FSTypeLink
	default:
		e.Type = agentprotocol.FSTypeOther
	}
	if t, err := time.Parse(time.RFC3339Nano, n.MTime); err == nil {
		e.MTime = t.Unix()
	}
	return e
}

// ListFiles lists one directory of a snapshot's volume: directories first,
// then by name. truncated is set when the directory has more than
// agentprotocol.VolFileListMax entries.
func (r *Runner) ListFiles(ctx context.Context, req agentprotocol.VolFileRequest, logf func(string)) (entries []agentprotocol.FSEntry, truncated bool, err error) {
	if logf == nil {
		logf = nopLog
	}
	p, perr := agentprotocol.CleanVolumePath(req.Path)
	if perr != nil {
		return nil, false, fileErr(agentprotocol.FSErrFailed, "%v", perr)
	}
	h, untrack, err := r.fileHelper(ctx, req, logf)
	if err != nil {
		return nil, false, err
	}
	defer untrack()
	defer h.close()

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	target := snapshotPath(req.Volume, p)
	col := &lsCollector{self: target, max: agentprotocol.VolFileListMax, Stop: cancel}
	code, msg, execErr := h.restic(lctx, col, "ls", "--json", req.SnapshotID, target)
	if col.Truncated {
		execErr, code = nil, 0
	}
	switch {
	case execErr != nil:
		return nil, false, fileErr(agentprotocol.FSErrFailed, "list files: %v", execErr)
	case code != 0:
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			return nil, false, fileErr(agentprotocol.FSErrNotFound, "no such file or directory")
		}
		return nil, false, fileErr(agentprotocol.FSErrFailed, "list files: restic exit %d: %s", code, clip(msg, 300))
	}
	for _, n := range col.nodes {
		if n.Path == target && n.Type != "dir" { // listing a file: nothing to list
			return nil, false, fileErr(agentprotocol.FSErrNotDir, "%s is not a directory", p)
		}
		entries = append(entries, entryOf(n))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].Type == agentprotocol.FSTypeDir, entries[j].Type == agentprotocol.FSTypeDir
		if di != dj {
			return di
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, col.Truncated, nil
}

// fileStream is the body of a file or archive read. Close stops restic and
// removes the helper.
type fileStream struct {
	io.Reader
	once    sync.Once
	closeFn func()
}

func (f *fileStream) Close() error { f.once.Do(f.closeFn); return nil }

// OpenFile starts reading a file (VolFileRead) or a directory as a zip archive
// (VolFileArchive). The entry's Size is -1 for an archive (unknown). The caller
// must Close the stream, also after it was read to EOF.
func (r *Runner) OpenFile(ctx context.Context, req agentprotocol.VolFileRequest, logf func(string)) (agentprotocol.FSEntry, io.ReadCloser, error) {
	if logf == nil {
		logf = nopLog
	}
	p, perr := agentprotocol.CleanVolumePath(req.Path)
	if perr != nil {
		return agentprotocol.FSEntry{}, nil, fileErr(agentprotocol.FSErrFailed, "%v", perr)
	}
	h, untrack, err := r.fileHelper(ctx, req, logf)
	if err != nil {
		return agentprotocol.FSEntry{}, nil, err
	}
	rctx, cancel := context.WithCancel(ctx)
	closeAll := func() { cancel(); h.close(); untrack() }
	fail := func(e error) (agentprotocol.FSEntry, io.ReadCloser, error) {
		closeAll()
		return agentprotocol.FSEntry{}, nil, e
	}

	target := snapshotPath(req.Volume, p)
	var out bytes.Buffer
	code, msg, execErr := h.restic(rctx, &out, "ls", "--json", req.SnapshotID, target)
	if execErr != nil {
		return fail(fileErr(agentprotocol.FSErrFailed, "stat: %v", execErr))
	}
	if code != 0 {
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			return fail(fileErr(agentprotocol.FSErrNotFound, "no such file or directory"))
		}
		return fail(fileErr(agentprotocol.FSErrFailed, "stat: restic exit %d: %s", code, clip(msg, 300)))
	}
	var node *lsNode
	for _, n := range parseLs(out.Bytes(), "") {
		if n.Path == target {
			n := n
			node = &n
			break
		}
	}
	if node == nil {
		return fail(fileErr(agentprotocol.FSErrNotFound, "no such file or directory"))
	}

	args := []string{"dump"}
	entry := entryOf(*node)
	switch {
	case req.Op == agentprotocol.VolFileArchive && node.Type == "dir":
		args = append(args, "--archive", "zip")
		name := node.Name
		if p == "/" {
			name = req.Volume
		}
		entry = agentprotocol.FSEntry{Name: name + ".zip", Type: agentprotocol.FSTypeFile, Size: -1, Mode: "0644"}
	case node.Type == "dir":
		return fail(fileErr(agentprotocol.FSErrIsDir, "%s is a directory", p))
	case node.Type != "file":
		return fail(fileErr(agentprotocol.FSErrFailed, "%s is not a regular file", p))
	default:
		limit := req.MaxBytes
		if limit <= 0 || limit > agentprotocol.MaxFileReadBytes {
			limit = agentprotocol.MaxFileReadBytes
		}
		if node.Size > limit {
			return fail(fileErr(agentprotocol.FSErrTooLarge, "file is %d bytes; the limit is %d", node.Size, limit))
		}
	}
	args = append(args, req.SnapshotID, target)

	pr, pw := io.Pipe()
	go func() {
		code, msg, err := h.restic(rctx, pw, args...)
		switch {
		case err != nil:
			_ = pw.CloseWithError(err)
		case code != 0:
			_ = pw.CloseWithError(fmt.Errorf("restic dump exited with status %d: %s", code, clip(msg, 300)))
		default:
			_ = pw.Close()
		}
	}()
	return entry, &fileStream{Reader: pr, closeFn: func() { _ = pr.Close(); closeAll() }}, nil
}
