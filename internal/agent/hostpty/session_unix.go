//go:build linux || darwin

package hostpty

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Session is a running process with a pseudo terminal.
type Session struct {
	master *os.File
	cmd    *exec.Cmd
	done   chan struct{} // closed when the process has been reaped
	code   int

	closeOnce sync.Once
}

// Start runs spec.Argv on a new pseudo terminal. The caller must Close the
// session.
func Start(spec Spec) (*Session, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("hostpty: empty argv")
	}
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	if err := setSize(master, spec.Cols, spec.Rows); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, err
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own whose controlling terminal is the slave (it is
	// stdin, fd 0 of the child).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, err
	}
	_ = slave.Close() // the child holds it now; the master reads EOF when it exits
	s := &Session{master: master, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		s.code = exitCode(cmd, err)
		close(s.done)
	}()
	return s, nil
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return -1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// Read returns the terminal's output. It returns io.EOF once the process and
// every descendant holding the terminal are gone.
func (s *Session) Read(p []byte) (int, error) {
	n, err := s.master.Read(p)
	if err != nil && errors.Is(err, syscall.EIO) {
		err = io.EOF // Linux reports a hung-up pty as EIO
	}
	return n, err
}

// Write sends input to the terminal.
func (s *Session) Write(p []byte) (int, error) { return s.master.Write(p) }

// Resize changes the terminal size.
func (s *Session) Resize(_ context.Context, cols, rows int) error {
	return setSize(s.master, cols, rows)
}

// Wait blocks until the process ended and returns its exit code (128+n when a
// signal n ended it).
func (s *Session) Wait(ctx context.Context) (int, error) {
	select {
	case <-s.done:
		return s.code, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

// Close ends the session: the terminal goes away (the shell gets SIGHUP), the
// process group is hung up at once and killed if it ignores that. Safe to call
// more than once.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		_ = s.master.Close()
		select {
		case <-s.done:
			return
		default:
		}
		pid := s.cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGHUP) // Setsid made the child its group's leader
		go func() {
			select {
			case <-s.done:
			case <-time.After(killGrace):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}()
	})
	return nil
}

type winsize struct{ Row, Col, X, Y uint16 }

func setSize(f *os.File, cols, rows int) error {
	ws := winsize{Row: uint16(max(rows, 1)), Col: uint16(max(cols, 1))}
	return ioctl(f, syscall.TIOCSWINSZ, unsafe.Pointer(&ws)) //nolint:gosec // ioctl needs a pointer to the winsize
}

// ioctl runs a request on f without File.Fd, which would switch the file to
// blocking mode and make Close unable to interrupt a pending Read.
func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
