package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// cancelGrace is how long a canceled compose command gets after SIGINT before
// it is killed.
const cancelGrace = 10 * time.Second

// Exec runs the docker CLI's compose plugin. The zero value is ready: it finds
// `docker` on PATH when a command runs, so constructing it never fails on a
// host without Docker Compose (only the commands do).
type Exec struct {
	// Bin is the docker binary ("docker" by default).
	Bin string
	// Environ lists the agent's environment (os.Environ by default); the
	// clean environment of a command is derived from it.
	Environ func() []string
}

// New returns an Exec for the docker CLI on PATH.
func New() *Exec { return &Exec{} }

var _ Runner = (*Exec)(nil)

func (x *Exec) bin() string {
	if x.Bin != "" {
		return x.Bin
	}
	return "docker"
}

func (x *Exec) agentEnv() []string {
	if x.Environ != nil {
		return x.Environ()
	}
	return os.Environ()
}

// agentKeys are copied from the agent's environment into every compose
// command: where the binary and the plugin live, where the registry
// credentials are, and which daemon to talk to.
var agentKeys = []string{
	"PATH", "HOME", "DOCKER_CONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
}

// blockedPrefixes and blockedKeys name the stack variables that must never
// reach the docker CLI: it runs as the agent's user (root), so a variable that
// changes how a process is loaded or which code it runs is a privilege
// escalation, and the ones listed after those also redirect the build backend
// or the credential helpers. Anything else is the stack's own configuration
// for ${VAR} interpolation (NODE_ENV, SSH_PORT, TZ, proxies, ...).
var (
	blockedPrefixes = []string{
		"DOCKER_", "COMPOSE_", // the daemon and compose's behaviour
		"LD_", "DYLD_", "MALLOC_", // dynamic linker and allocator hooks
		"BUILDX_", "BUILDKIT_", // build backend
	}
	blockedKeys = map[string]bool{
		"GCONV_PATH": true, "LOCPATH": true, "NLSPATH": true, "HOSTALIASES": true, "RES_OPTIONS": true, "LOCALDOMAIN": true,
		"BASH_ENV": true, "SHELLOPTS": true, "BASHOPTS": true, "PS4": true, "IFS": true, "CDPATH": true,
		"NODE_OPTIONS": true, "NODE_PATH": true, "PYTHONPATH": true, "PYTHONSTARTUP": true, "PYTHONHOME": true,
		"PERL5OPT": true, "PERL5LIB": true, "PERLLIB": true, "RUBYOPT": true, "RUBYLIB": true,
		"JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true, "LUA_PATH": true, "LUA_CPATH": true,
		"SSH_AUTH_SOCK": true, "DBUS_SESSION_BUS_ADDRESS": true, "GNUPGHOME": true, "PASSWORD_STORE_DIR": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_RUNTIME_DIR": true,
		"PATH": true, "HOME": true, // the agent's own values are the only ones used
	}
)

// blockedStackKey reports whether a stack variable is withheld from compose.
// COMPOSE_PROFILES is the one COMPOSE_ setting a stack may choose.
func blockedStackKey(k string) bool {
	if k == "COMPOSE_PROFILES" {
		return false
	}
	if blockedKeys[k] {
		return true
	}
	for _, p := range blockedPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// CleanEnv builds the environment of a compose command. The agent's own
// environment never reaches compose (it holds the enrollment token and other
// agent settings): only agentKeys are taken from it. user are the stack's
// variables as KEY=VALUE; keys that blockedStackKey withholds are dropped
// (a variable must not redirect the daemon, change compose's behaviour or
// inject code into the root process), and the agent's own values win over a
// user key of the same name.
func CleanEnv(agent, user []string) []string {
	have := make(map[string]string, len(agent))
	for _, kv := range agent {
		if k, v, ok := strings.Cut(kv, "="); ok {
			have[k] = v
		}
	}
	var out []string
	for _, kv := range user {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || blockedStackKey(k) {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range agentKeys {
		if v, ok := have[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// global returns the arguments before the subcommand.
func global(p Project, extra ...string) []string {
	args := []string{"compose", "--ansi", "never", "--progress", "plain"}
	args = append(args, extra...)
	if p.Name != "" {
		args = append(args, "-p", p.Name)
	}
	for _, f := range p.Files {
		args = append(args, "-f", f)
	}
	if len(p.Files) > 0 && p.Dir != "" {
		args = append(args, "--project-directory", p.Dir)
	}
	return args
}

func seconds(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}

// Version implements Runner.
func (x *Exec) Version(ctx context.Context) (string, error) {
	var stdout bytes.Buffer
	if err := x.run(ctx, Project{}, []string{"compose", "version", "--short"}, &stdout, nil); err != nil {
		return "", err
	}
	v := strings.TrimSpace(stdout.String())
	if v == "" {
		return "", errors.New("docker compose version printed nothing")
	}
	return v, nil
}

// Config implements Runner. Every profile is enabled so the model lists every
// service of the file, the set the control plane derives its components from.
func (x *Exec) Config(ctx context.Context, p Project, out Output) ([]byte, error) {
	var stdout bytes.Buffer
	args := append(global(p, "--profile", "*"), "config", "--format", "json")
	if err := x.run(ctx, p, args, &stdout, out); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Pull implements Runner.
func (x *Exec) Pull(ctx context.Context, p Project, out Output) error {
	return x.run(ctx, p, append(global(p), "pull"), nil, out)
}

// Up implements Runner.
func (x *Exec) Up(ctx context.Context, p Project, o UpOptions, out Output) error {
	args := append(global(p), "up", "-d")
	if o.RemoveOrphans {
		args = append(args, "--remove-orphans")
	}
	if o.Wait {
		args = append(args, "--wait")
		if o.WaitTimeout > 0 {
			args = append(args, "--wait-timeout", seconds(o.WaitTimeout))
		}
	}
	return x.run(ctx, p, args, nil, out)
}

// Restart implements Runner.
func (x *Exec) Restart(ctx context.Context, p Project, grace time.Duration, out Output) error {
	args := append(global(Project{Name: p.Name}), "restart")
	if grace > 0 {
		args = append(args, "-t", seconds(grace))
	}
	return x.run(ctx, p, args, nil, out)
}

// Stop implements Runner.
func (x *Exec) Stop(ctx context.Context, p Project, grace time.Duration, out Output) error {
	args := append(global(Project{Name: p.Name}), "stop")
	if grace > 0 {
		args = append(args, "-t", seconds(grace))
	}
	return x.run(ctx, p, args, nil, out)
}

// Down implements Runner.
func (x *Exec) Down(ctx context.Context, p Project, o DownOptions, out Output) error {
	args := append(global(Project{Name: p.Name}), "down", "--remove-orphans")
	if o.RemoveVolumes {
		args = append(args, "--volumes")
	}
	if o.Grace > 0 {
		args = append(args, "-t", seconds(o.Grace))
	}
	return x.run(ctx, p, args, nil, out)
}

// run executes docker with args in p.Dir under a clean environment. stdout,
// when set, receives the command's stdout; everything else (and stderr) goes
// to out line by line. A failure carries the last lines of output so the
// caller's error is self-explanatory.
func (x *Exec) run(ctx context.Context, p Project, args []string, stdout *bytes.Buffer, out Output) error {
	cmd := exec.CommandContext(ctx, x.bin(), args...)
	cmd.Dir = p.Dir
	cmd.Env = CleanEnv(x.agentEnv(), p.Env)
	// A canceled command gets SIGINT first, as the docker CLI treats it as
	// "stop what you are doing" (compose aborts the wait and leaves the
	// containers as they are); after cancelGrace it is killed.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = cancelGrace

	tail := &tailLines{max: 8}
	lw := &lineWriter{fn: func(line string) {
		tail.add(line)
		if out != nil {
			out(line)
		}
	}}
	cmd.Stderr = lw
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = lw
	}
	err := cmd.Run()
	lw.flush()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	what := "docker " + strings.Join(subcommand(args), " ")
	if t := tail.String(); t != "" {
		return fmt.Errorf("%s: %w: %s", what, err, t)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// subcommand picks the words of args that name the command ("compose up"),
// without flags and their values, for error messages.
func subcommand(args []string) []string {
	out := []string{"compose"}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--ansi", "--progress", "--profile", "-p", "-f", "--project-directory":
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				return append(out, args[i])
			}
		}
	}
	return out
}

// lineWriter splits what is written to it into lines.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 64<<10 { // a line without end: do not grow without bound
		w.emit(w.buf)
		w.buf = nil
	}
	return len(b), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

func (w *lineWriter) emit(b []byte) {
	// Progress output redraws lines with carriage returns: keep the last frame.
	line := string(b)
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	if line = strings.TrimRight(line, " \t\r"); line != "" {
		w.fn(line)
	}
}

// tailLines keeps the last max lines.
type tailLines struct {
	max   int
	lines []string
}

func (t *tailLines) add(l string) {
	t.lines = append(t.lines, l)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *tailLines) String() string { return strings.Join(t.lines, "; ") }
