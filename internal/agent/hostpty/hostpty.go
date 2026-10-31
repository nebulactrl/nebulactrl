// Package hostpty runs a process on a pseudo terminal of the machine the agent
// itself runs on: the host shell of an agent installed directly on the host.
//
// A Session implements docker.TTYSession (Read, Write, Resize, Wait, Close), so
// the agent's terminal relay treats it like a container exec. Only Linux and
// macOS are supported; the standard library is enough (no pty dependency):
// open /dev/ptmx, unlock the slave, start the child on it as session leader.
package hostpty

import (
	"errors"
	"os"
	"os/user"
	"strings"
	"time"
)

// ErrUnsupported is returned on platforms without pseudo-terminal support.
var ErrUnsupported = errors.New("hostpty: pseudo terminals are not supported on this platform")

// killGrace is how long a closed session's process group may take to react
// to SIGHUP before it is killed.
const killGrace = 2 * time.Second

// Spec configures Start.
type Spec struct {
	// Argv is the program and its arguments (no shell parsing).
	Argv []string
	// Env is KEY=VALUE pairs; the child gets exactly these (nothing of the
	// agent's own environment, which holds its enrolment token and URLs).
	Env []string
	// Dir is the working directory.
	Dir string
	// Cols and Rows are the initial terminal size.
	Cols, Rows int
}

// Env builds the environment of a host shell: a minimal, explicit one. The
// agent's own variables (enrolment token, control-plane URL) are not passed on.
func Env(term, shell string) []string {
	home, name := "/", "root"
	if u, err := user.Current(); err == nil {
		if u.HomeDir != "" {
			home = u.HomeDir
		}
		if u.Username != "" {
			name = u.Username
		}
	}
	if h := os.Getenv("HOME"); h != "" {
		home = h
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env := []string{
		"TERM=" + term, "COLORTERM=truecolor", "HOME=" + home, "USER=" + name, "LOGNAME=" + name,
		"PATH=" + path, "SHELL=" + shell,
	}
	for _, k := range []string{"LANG", "LC_ALL"} {
		if v := os.Getenv(k); v != "" && !strings.ContainsAny(v, "\n\x00") {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// HomeDir returns the directory a host shell starts in.
func HomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return "/"
}
