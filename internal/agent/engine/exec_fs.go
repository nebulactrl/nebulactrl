package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The file browser. Every operation goes through the Docker API (exec for
// listings, the archive endpoints for reads and writes), so paths resolve
// inside the container's own filesystem and the host filesystem is never
// touched. Reads and uploads are streamed in fs_data frames under the stream
// flow control of exec.go.

const (
	maxPathLen = 4096
	// maxLinkHops bounds symlink resolution of one path.
	maxLinkHops = 8
	// maxTarListBytes bounds the archive bytes a listing without a shell reads.
	maxTarListBytes = 64 << 20
	// uploadIdle is how long an upload may wait for its next chunk.
	uploadIdle = 60 * time.Second
	// uploadQueue bounds the chunks buffered for one upload (the window keeps
	// a well-behaved sender far below it).
	uploadQueue = 32
)

// listScript prints one line per entry of the directory $1:
// type TAB size TAB mtime TAB mode TAB name TAB linktarget. It needs only a
// POSIX sh; stat and readlink are used when the image has them.
const listScript = `cd "$1" 2>/dev/null || exit 3
T=$(printf '\t')
for f in .[!.]* ..?* *; do
	[ -e "./$f" ] || [ -L "./$f" ] || continue
	l=$(stat -c "%F${T}%s${T}%Y${T}%a" "./$f" 2>/dev/null)
	if [ -z "$l" ]; then
		if [ -L "./$f" ]; then l="symbolic link${T}0${T}0${T}0"
		elif [ -d "./$f" ]; then l="directory${T}0${T}0${T}0"
		elif [ -f "./$f" ]; then l="regular file${T}0${T}0${T}0"
		else l="other${T}0${T}0${T}0"; fi
	fi
	r=
	if [ -L "./$f" ]; then r=$(readlink "./$f" 2>/dev/null); fi
	printf '%s\t%s\t%s\n' "$l" "$f" "$r"
done`

// CleanContainerPath validates and normalizes a container path: absolute, no
// NUL, bounded. ".." resolves lexically against the container root; symlinks
// are resolved by the daemon inside the container's own root.
func CleanContainerPath(p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("path is empty")
	case len(p) > maxPathLen:
		return "", errors.New("path is too long")
	case strings.ContainsRune(p, 0):
		return "", errors.New("path contains a NUL byte")
	case !strings.HasPrefix(p, "/"):
		return "", errors.New("path must be absolute")
	}
	return path.Clean(p), nil
}

// upload is the receive side of one FSWrite.
type upload struct {
	ch     chan agentprotocol.FSData
	cancel context.CancelFunc
}

// startFS runs one file operation on its own goroutine.
func (e *Engine) startFS(req agentprotocol.FSRequest) {
	ctx, cancel := context.WithCancel(context.Background())
	e.term.mu.Lock()
	if e.term.cancels == nil {
		e.term.cancels = map[string]context.CancelFunc{}
	}
	e.term.cancels[req.RequestID] = cancel
	e.term.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.term.mu.Lock()
			delete(e.term.cancels, req.RequestID)
			e.term.mu.Unlock()
		}()
		e.handleFS(ctx, cancel, req)
	}()
}

func fsFailure(err error) (code, msg string) {
	switch {
	case errors.Is(err, docker.ErrNotFound):
		return agentprotocol.FSErrNotFound, "no such file or directory"
	case errors.Is(err, errNoReplica):
		return agentprotocol.FSErrNoReplica, "the replica is not running"
	}
	return agentprotocol.FSErrFailed, err.Error()
}

func (e *Engine) handleFS(ctx context.Context, cancel context.CancelFunc, req agentprotocol.FSRequest) {
	send := func(typ string, body any) bool { return e.stream.Stream(typ, req.RequestID, body) }
	respond := func(r agentprotocol.FSResponse) {
		r.RequestID = req.RequestID
		send(agentprotocol.TypeFSResponse, r)
	}
	fail := func(code, msg string) { respond(agentprotocol.FSResponse{Code: code, Error: msg}) }

	td, ok := e.terminalDocker()
	if !ok {
		fail(agentprotocol.FSErrFailed, "this agent's Docker engine does not support the file browser")
		return
	}
	p, err := CleanContainerPath(req.Path)
	if err != nil {
		fail(agentprotocol.FSErrFailed, err.Error())
		return
	}
	ctr, err := e.replicaContainer(ctx, req.ServiceID, req.Process, req.Replica)
	if err != nil {
		fail(agentprotocol.FSErrNoReplica, noReplicaMessage(req.Process, req.Replica))
		return
	}
	switch req.Op {
	case agentprotocol.FSList:
		e.fsList(ctx, td, ctr, p, respond)
	case agentprotocol.FSRead:
		e.fsRead(ctx, td, ctr, p, req, send, respond)
	case agentprotocol.FSWrite:
		e.fsWrite(ctx, cancel, td, ctr, p, req, send, respond)
	default:
		fail(agentprotocol.FSErrFailed, "unknown file operation "+strconv.Quote(req.Op))
	}
}

// resolveLinks stats p, following symlinks (the daemon's stat does not follow
// the final element) up to maxLinkHops.
func resolveLinks(ctx context.Context, td docker.Terminal, id, p string) (docker.PathStat, string, error) {
	for range maxLinkHops {
		st, err := td.ContainerStatPath(ctx, id, p)
		if err != nil {
			return docker.PathStat{}, "", err
		}
		if st.LinkTarget == "" && st.Mode&os.ModeSymlink == 0 {
			return st, p, nil
		}
		target := st.LinkTarget
		if target == "" {
			return docker.PathStat{}, "", errors.New("dangling symbolic link")
		}
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(p), target)
		}
		p = path.Clean(target)
	}
	return docker.PathStat{}, "", errors.New("too many levels of symbolic links")
}

func sortEntries(es []agentprotocol.FSEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		di, dj := es[i].Type == agentprotocol.FSTypeDir, es[j].Type == agentprotocol.FSTypeDir
		if di != dj {
			return di
		}
		return strings.ToLower(es[i].Name) < strings.ToLower(es[j].Name)
	})
}

var errListFull = errors.New("listing full")

// listWriter parses listScript output line by line; it stops the exec once
// MaxListEntries entries were read.
type listWriter struct {
	buf     []byte
	entries []agentprotocol.FSEntry
	full    bool
}

func (w *listWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if ent, ok := parseListLine(line); ok {
			if len(w.entries) >= agentprotocol.MaxListEntries {
				w.full = true
				return len(p), errListFull
			}
			w.entries = append(w.entries, ent)
		}
	}
	if len(w.buf) > 64<<10 { // a "line" without a newline: not our output
		w.buf = nil
	}
	return len(p), nil
}

// parseListLine parses "type\tsize\tmtime\tmode\tname\ttarget".
func parseListLine(line string) (agentprotocol.FSEntry, bool) {
	f := strings.SplitN(line, "\t", 6)
	if len(f) < 5 || f[4] == "" {
		return agentprotocol.FSEntry{}, false
	}
	t := agentprotocol.FSTypeOther
	switch {
	case strings.Contains(f[0], "directory"):
		t = agentprotocol.FSTypeDir
	case strings.Contains(f[0], "symbolic link"):
		t = agentprotocol.FSTypeLink
	case strings.Contains(f[0], "regular"):
		t = agentprotocol.FSTypeFile
	}
	size, _ := strconv.ParseInt(f[1], 10, 64)
	mtime, _ := strconv.ParseInt(f[2], 10, 64)
	mode := ""
	if m, err := strconv.ParseUint(f[3], 8, 32); err == nil {
		mode = fmt.Sprintf("%04o", m)
	}
	ent := agentprotocol.FSEntry{Name: f[4], Type: t, Size: size, Mode: mode, MTime: mtime}
	if len(f) == 6 {
		ent.LinkTarget = f[5]
	}
	return ent, true
}

func (e *Engine) fsList(ctx context.Context, td docker.Terminal, ctr docker.ContainerInfo, p string, respond func(agentprotocol.FSResponse)) {
	st, rp, err := resolveLinks(ctx, td, ctr.ID, p)
	if err != nil {
		code, msg := fsFailure(err)
		respond(agentprotocol.FSResponse{Code: code, Error: msg})
		return
	}
	if !st.Mode.IsDir() {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrNotDir, Error: p + " is not a directory"})
		return
	}
	lw := &listWriter{}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, err := e.cfg.Docker.ContainerExec(lctx, ctr.ID, []string{"sh", "-c", listScript, "sh", rp}, docker.ExecSpec{}, lw, io.Discard)
	switch {
	case lw.full || errors.Is(err, errListFull):
		sortEntries(lw.entries)
		respond(agentprotocol.FSResponse{Stage: agentprotocol.FSDone, Path: rp, Entries: lw.entries, Truncated: true})
		return
	case err == nil && code == 0:
		sortEntries(lw.entries)
		respond(agentprotocol.FSResponse{Stage: agentprotocol.FSDone, Path: rp, Entries: lw.entries})
		return
	case err == nil && code == 3:
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrPermission, Error: "permission denied"})
		return
	}
	// No usable shell (distroless, scratch): read the directory's archive instead.
	entries, truncated, terr := tarList(ctx, td, ctr.ID, rp)
	if terr != nil {
		c, msg := fsFailure(terr)
		respond(agentprotocol.FSResponse{Code: c, Error: msg})
		return
	}
	sortEntries(entries)
	respond(agentprotocol.FSResponse{Stage: agentprotocol.FSDone, Path: rp, Entries: entries, Truncated: truncated})
}

// tarList lists a directory from its archive: the immediate children only. It
// reads at most maxTarListBytes of archive, so a huge tree reports Truncated.
func tarList(ctx context.Context, td docker.Terminal, id, dir string) ([]agentprotocol.FSEntry, bool, error) {
	rc, _, err := td.CopyFromContainer(ctx, id, strings.TrimSuffix(dir, "/")+"/.")
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	lr := &io.LimitedReader{R: rc, N: maxTarListBytes}
	tr := tar.NewReader(lr)
	var out []agentprotocol.FSEntry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, false, nil
		}
		if err != nil {
			if lr.N <= 0 {
				return out, true, nil
			}
			return nil, false, fmt.Errorf("read directory archive: %w", err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "./"), "/")
		if name == "" || name == "." || strings.Contains(name, "/") {
			continue
		}
		if len(out) >= agentprotocol.MaxListEntries {
			return out, true, nil
		}
		t := agentprotocol.FSTypeOther
		switch hdr.Typeflag {
		case tar.TypeDir:
			t = agentprotocol.FSTypeDir
		case tar.TypeReg:
			t = agentprotocol.FSTypeFile
		case tar.TypeSymlink:
			t = agentprotocol.FSTypeLink
		}
		out = append(out, agentprotocol.FSEntry{
			Name: name, Type: t, Size: hdr.Size, Mode: fmt.Sprintf("%04o", hdr.Mode&0o7777),
			MTime: hdr.ModTime.Unix(), LinkTarget: hdr.Linkname,
		})
	}
}

func (e *Engine) fsRead(ctx context.Context, td docker.Terminal, ctr docker.ContainerInfo, p string, req agentprotocol.FSRequest,
	send func(string, any) bool, respond func(agentprotocol.FSResponse)) {
	st, rp, err := resolveLinks(ctx, td, ctr.ID, p)
	if err != nil {
		code, msg := fsFailure(err)
		respond(agentprotocol.FSResponse{Code: code, Error: msg})
		return
	}
	if st.Mode.IsDir() {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrIsDir, Error: p + " is a directory"})
		return
	}
	limit := req.MaxBytes
	if limit <= 0 || limit > agentprotocol.MaxFileReadBytes {
		limit = agentprotocol.MaxFileReadBytes
	}
	if st.Size > limit {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrTooLarge, Error: fmt.Sprintf("file is %d bytes; the limit is %d", st.Size, limit)})
		return
	}
	rc, _, err := td.CopyFromContainer(ctx, ctr.ID, rp)
	if err != nil {
		code, msg := fsFailure(err)
		respond(agentprotocol.FSResponse{Code: code, Error: msg})
		return
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrFailed, Error: "read file archive: " + err.Error()})
		return
	}
	if hdr.Typeflag != tar.TypeReg {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrFailed, Error: p + " is not a regular file"})
		return
	}
	ent := agentprotocol.FSEntry{
		Name: path.Base(rp), Type: agentprotocol.FSTypeFile, Size: hdr.Size, Mode: fmt.Sprintf("%04o", hdr.Mode&0o7777), MTime: hdr.ModTime.Unix(),
	}
	gate := newCreditGate()
	e.term.mu.Lock()
	if e.term.gates == nil {
		e.term.gates = map[string]*creditGate{}
	}
	e.term.gates[req.RequestID] = gate
	e.term.mu.Unlock()
	defer func() {
		gate.Close()
		e.term.mu.Lock()
		delete(e.term.gates, req.RequestID)
		e.term.mu.Unlock()
	}()

	respond(agentprotocol.FSResponse{Stage: agentprotocol.FSReady, Path: rp, Entry: &ent})
	buf := make([]byte, agentprotocol.FSChunk)
	var sent int64
	for {
		n, rerr := tr.Read(buf)
		if n > 0 {
			sent += int64(n)
			if sent > limit {
				send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Error: "file grew beyond the read limit"})
				return
			}
			if gate.Acquire(ctx, n) != nil {
				return
			}
			if !send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: append([]byte(nil), buf[:n]...)}) {
				return
			}
		}
		if errors.Is(rerr, io.EOF) {
			send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, EOF: true})
			return
		}
		if rerr != nil {
			send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Error: "read file: " + rerr.Error()})
			return
		}
	}
}

func (e *Engine) fsWrite(ctx context.Context, cancel context.CancelFunc, td docker.Terminal, ctr docker.ContainerInfo, p string, req agentprotocol.FSRequest,
	send func(string, any) bool, respond func(agentprotocol.FSResponse)) {
	dir, name := path.Split(p)
	if name == "" || name == "." || name == ".." {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrFailed, Error: "the path must name a file"})
		return
	}
	if req.Size < 0 || req.Size > agentprotocol.MaxUploadBytes {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrTooLarge, Error: fmt.Sprintf("uploads are limited to %d bytes", agentprotocol.MaxUploadBytes)})
		return
	}
	st, rdir, err := resolveLinks(ctx, td, ctr.ID, path.Clean(dir))
	if err != nil {
		code, msg := fsFailure(err)
		if code == agentprotocol.FSErrNotFound {
			msg = "the target directory does not exist"
		}
		respond(agentprotocol.FSResponse{Code: code, Error: msg})
		return
	}
	if !st.Mode.IsDir() {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrNotDir, Error: path.Clean(dir) + " is not a directory"})
		return
	}
	up := &upload{ch: make(chan agentprotocol.FSData, uploadQueue), cancel: cancel}
	e.term.mu.Lock()
	if e.term.uploads == nil {
		e.term.uploads = map[string]*upload{}
	}
	e.term.uploads[req.RequestID] = up
	e.term.mu.Unlock()
	defer func() {
		e.term.mu.Lock()
		delete(e.term.uploads, req.RequestID)
		e.term.mu.Unlock()
	}()

	mode := int64(req.Mode & 0o7777)
	if mode == 0 {
		mode = 0o644
	}
	// The whole tar is assembled before the daemon is touched: an upload that
	// aborts, overruns its announced size or times out must leave nothing in
	// the container (a streamed, truncated tar can still create the file).
	respond(agentprotocol.FSResponse{Stage: agentprotocol.FSReady, Path: path.Join(rdir, name)})
	var tarball bytes.Buffer
	werr := func() error {
		tw := tar.NewWriter(&tarball)
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: req.Size, ModTime: time.Now()}); err != nil {
			return err
		}
		var got int64
		for {
			var d agentprotocol.FSData
			select {
			case d = <-up.ch:
			case <-ctx.Done():
				return errors.New("upload canceled")
			case <-time.After(uploadIdle):
				return errors.New("upload timed out waiting for data")
			}
			if d.Error != "" {
				return errors.New(d.Error)
			}
			if got+int64(len(d.Data)) > req.Size {
				return errors.New("more data than announced")
			}
			if len(d.Data) > 0 {
				if _, err := tw.Write(d.Data); err != nil {
					return err
				}
				got += int64(len(d.Data))
				send(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: req.RequestID, Bytes: len(d.Data)})
			}
			if d.EOF {
				break
			}
		}
		if got != req.Size {
			return fmt.Errorf("received %d bytes, expected %d", got, req.Size)
		}
		return tw.Close()
	}()
	if werr != nil {
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrFailed, Error: werr.Error()})
		return
	}
	if cerr := e.cfg.Docker.CopyToContainer(ctx, ctr.ID, rdir, &tarball); cerr != nil {
		code, msg := fsFailure(cerr)
		respond(agentprotocol.FSResponse{Code: code, Error: msg})
		return
	}
	respond(agentprotocol.FSResponse{Stage: agentprotocol.FSDone, Path: path.Join(rdir, name)})
}

// uploadData hands a chunk to its upload; it never blocks the reader goroutine.
func (e *Engine) uploadData(d agentprotocol.FSData) {
	e.term.mu.Lock()
	up := e.term.uploads[d.RequestID]
	e.term.mu.Unlock()
	if up == nil {
		return
	}
	select {
	case up.ch <- d:
	default:
		up.cancel() // the sender ignored the window
	}
}
