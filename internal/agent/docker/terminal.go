package docker

import (
	"context"
	"io"
	"os"
	"time"
)

// Terminal is the optional interactive surface of the daemon: exec with a TTY
// and the archive API the file browser reads and writes through. SDKEngine
// implements it; the engine type-asserts it so tests and other Engine
// implementations are not forced to.
type Terminal interface {
	// ContainerExecTTY starts argv in a running container with a pseudo
	// terminal attached. The caller must Close the session.
	ContainerExecTTY(ctx context.Context, id string, spec TTYSpec) (TTYSession, error)
	// ContainerStatPath stats a path inside the container without following a
	// final symlink (LinkTarget is set for links). Wraps ErrNotFound.
	ContainerStatPath(ctx context.Context, id, path string) (PathStat, error)
	// CopyFromContainer returns the tar archive of path (a file, or a
	// directory with its whole tree) and its stat. The caller must Close it.
	CopyFromContainer(ctx context.Context, id, path string) (io.ReadCloser, PathStat, error)
}

// TTYSpec configures ContainerExecTTY.
type TTYSpec struct {
	Argv []string
	// Env is KEY=VALUE pairs for the process only.
	Env  []string
	User string
	// Cols and Rows are the initial terminal size.
	Cols, Rows int
}

// TTYSession is a running process with a pseudo terminal. Reads yield the
// terminal's output (one merged stream), writes are its input.
type TTYSession interface {
	io.Reader
	io.Writer
	// Resize changes the terminal size.
	Resize(ctx context.Context, cols, rows int) error
	// Wait blocks until the process ended and returns its exit code.
	Wait(ctx context.Context) (int, error)
	// Close drops the attachment. The process receives SIGHUP when its
	// terminal goes away; callers that must be sure kill it separately.
	Close() error
}

// PathStat is the stat of one path in a container.
type PathStat struct {
	Name       string
	Size       int64
	Mode       os.FileMode
	MTime      time.Time
	LinkTarget string
}
