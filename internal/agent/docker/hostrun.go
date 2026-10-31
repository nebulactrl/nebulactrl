package docker

import "context"

// HostCommandSpec describes a one-shot helper container that runs a program in
// the host's namespaces: privileged, sharing the host's PID namespace, whose
// command is `nsenter -t 1 -m -u -i -n -p -- <program>`.
type HostCommandSpec struct {
	Name   string
	Image  string
	Labels map[string]string
	// Argv is the container's command; it replaces the image's entrypoint.
	Argv []string
	Env  []string
	// MaxOutput bounds the captured stdout and stderr, in bytes each (default 4 MiB).
	MaxOutput int64
}

// HostCommandResult is what a host command printed and how it ended.
type HostCommandResult struct {
	Stdout, Stderr string
	ExitCode       int
	// Truncated is true when an output stream hit MaxOutput.
	Truncated bool
}

// HostCommandRunner is the optional surface that runs a non-interactive host
// command through a helper container. SDKEngine implements it; the engine
// type-asserts it so fakes are not forced to.
type HostCommandRunner interface {
	// RunHostCommand creates the helper with AutoRemove (the daemon removes it
	// when the program exits, even if the agent has died meanwhile), attaches
	// before starting so no output is lost, waits for the program to end and
	// returns what it printed. Cancelling ctx kills the helper. When it fails
	// nothing is left behind.
	RunHostCommand(ctx context.Context, spec HostCommandSpec) (HostCommandResult, error)
}
