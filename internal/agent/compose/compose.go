// Package compose runs `docker compose` for the agent's native Compose stacks.
//
// The engine depends on the Runner interface only; Exec is the production
// implementation (it executes the docker CLI with an argument vector and a
// clean environment), composetest.Fake the test one.
package compose

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinVersion is the oldest Docker Compose plugin that supports the `!reset` and
// `!override` merge tags the stack override uses.
const MinVersion = "2.24.0"

// Project identifies one compose project and what a command needs to run it.
type Project struct {
	// Name is the compose project name (-p), nc-<serviceId8>.
	Name string
	// Dir is the working directory of the command and the --project-directory
	// of commands that read files: the stack's directory.
	Dir string
	// Files are the compose files (-f), in order: the user's file, then the
	// generated override. Empty for commands that act on the project by name.
	Files []string
	// Env holds the stack's variables as KEY=VALUE, used for compose
	// interpolation only. The runner passes them in a clean environment and
	// drops DOCKER_* and COMPOSE_* keys (except COMPOSE_PROFILES).
	Env []string
}

// Output receives the output lines (stdout and stderr, in order) of a command
// while it runs.
type Output func(line string)

// UpOptions are the flags of `docker compose up -d`.
type UpOptions struct {
	// Wait adds --wait: return when every service runs and is healthy.
	Wait bool
	// WaitTimeout bounds --wait (--wait-timeout, whole seconds; 0 = no flag).
	WaitTimeout time.Duration
	// RemoveOrphans adds --remove-orphans.
	RemoveOrphans bool
}

// DownOptions are the flags of `docker compose down`.
type DownOptions struct {
	// RemoveVolumes adds --volumes: named and anonymous volumes go too.
	RemoveVolumes bool
	// Grace is the stop timeout (-t); 0 keeps compose's default.
	Grace time.Duration
}

// Runner is what the engine needs from Docker Compose. Every method honours
// ctx: a canceled context interrupts the running command.
type Runner interface {
	// Version returns `docker compose version --short`.
	Version(ctx context.Context) (string, error)
	// Config normalises the project (`config --format json`, every profile
	// enabled) and returns the JSON model. Warnings go to out.
	Config(ctx context.Context, p Project, out Output) ([]byte, error)
	// Pull pulls the images of the project's enabled services.
	Pull(ctx context.Context, p Project, out Output) error
	// Up creates and starts the project (`up -d`).
	Up(ctx context.Context, p Project, o UpOptions, out Output) error
	// Restart restarts the project's containers by project name (no files).
	Restart(ctx context.Context, p Project, grace time.Duration, out Output) error
	// Stop stops the project's containers by project name (no files).
	Stop(ctx context.Context, p Project, grace time.Duration, out Output) error
	// Down removes the project's containers and networks by project name
	// (`down --remove-orphans`, no files).
	Down(ctx context.Context, p Project, o DownOptions, out Output) error
}

// ParseVersion reads a `docker compose version --short` answer ("2.29.7",
// "v2.24.0", "2.27.0-desktop.1") into its numeric parts.
func ParseVersion(s string) (major, minor, patch int, err error) {
	v := strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(v, "-+ \t\r\n"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, fmt.Errorf("compose version %q is not major.minor[.patch]", s)
	}
	n := [3]int{}
	for i, p := range parts {
		x, perr := strconv.Atoi(p)
		if perr != nil || x < 0 {
			return 0, 0, 0, fmt.Errorf("compose version %q is not numeric", s)
		}
		n[i] = x
	}
	return n[0], n[1], n[2], nil
}

// AtLeastMin reports whether version satisfies MinVersion. An unparsable
// version does not.
func AtLeastMin(version string) bool {
	maj, min, pat, err := ParseVersion(version)
	if err != nil {
		return false
	}
	mmaj, mmin, mpat, _ := ParseVersion(MinVersion)
	switch {
	case maj != mmaj:
		return maj > mmaj
	case min != mmin:
		return min > mmin
	}
	return pat >= mpat
}
