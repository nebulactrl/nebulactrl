package docker

import "context"

// HostShellSpec describes the helper container of a host shell: a privileged
// container sharing the host's PID namespace whose command (nsenter into
// PID 1's namespaces) is the shell.
type HostShellSpec struct {
	Name   string
	Image  string
	Labels map[string]string
	// Argv is the container's command (the image has no entrypoint that matters:
	// it replaces it).
	Argv []string
	Env  []string
	// Cols and Rows are the initial terminal size.
	Cols, Rows int
}

// HostShellRunner is the optional surface that runs a host-shell helper
// container with a TTY. SDKEngine implements it; the engine type-asserts it so
// fakes are not forced to.
type HostShellRunner interface {
	// RunHostShell creates the helper (privileged, --pid=host, TTY, stdin kept
	// open until the attached client leaves, removed by the daemon when it
	// exits), attaches before starting so no output is lost, and starts it.
	// It returns the container ID and the attached session. When it fails
	// nothing is left behind. The caller removes the container (force) when
	// the session is over; the daemon's own removal on exit is a backstop.
	RunHostShell(ctx context.Context, spec HostShellSpec) (id string, tty TTYSession, err error)
}
