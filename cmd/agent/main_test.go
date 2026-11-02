package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// clearAgentEnv blanks every environment variable the agent reads, so the
// developer's own NEBULACTRL_* settings never leak into a test. applyEnv ignores
// empty values.
func clearAgentEnv(t *testing.T) {
	t.Helper()
	newCommand().Flags().VisitAll(func(f *pflag.Flag) { t.Setenv(envName(f.Name), "") })
	t.Setenv("NEBULACTRL_LOG_LEVEL", "")
}

func TestEnvName(t *testing.T) {
	for flag, want := range map[string]string{
		"url":                "NEBULACTRL_URL",
		"state-dir":          "NEBULACTRL_STATE_DIR",
		"network-prefix-len": "NEBULACTRL_NETWORK_PREFIX_LEN",
		"insecure-dev":       "NEBULACTRL_INSECURE_DEV",
		"token":              "NEBULACTRL_ENROLL_TOKEN",
	} {
		assert.Equal(t, want, envName(flag), flag)
	}
}

func TestNewCommandFlagDefaults(t *testing.T) {
	cmd := newCommand()
	assert.Equal(t, "nebulactrl-agent", cmd.Use)
	assert.Equal(t, version, cmd.Version)

	str := func(name string) string { v, _ := cmd.Flags().GetString(name); return v }
	num := func(name string) int { v, _ := cmd.Flags().GetInt(name); return v }
	assert.Equal(t, "", str("url"))
	assert.Equal(t, "", str("token"))
	assert.Equal(t, "/var/lib/nebulactrl-agent", str("state-dir"))
	assert.Equal(t, "", str("proxy-admin-url"))
	assert.Equal(t, agentprotocol.ProxyAdminPort, num("proxy-admin-port"))
	assert.Equal(t, 80, num("proxy-http-port"))
	assert.Equal(t, 443, num("proxy-https-port"))
	assert.Equal(t, "", str("backup-dir"))
	assert.Equal(t, "", str("agent-container"))
	assert.Equal(t, netpool.DefaultPool, str("network-pool"))
	assert.Equal(t, netpool.DefaultPrefixLen, num("network-prefix-len"))
	d, err := cmd.Flags().GetDuration("uptime-interval")
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.UptimeInterval, d)
	insecure, err := cmd.Flags().GetBool("insecure-dev")
	require.NoError(t, err)
	assert.False(t, insecure)
}

func TestNewCommandRegistersHardeningSubcommands(t *testing.T) {
	cmd := newCommand()
	hc, _, err := cmd.Find([]string{"hardening"})
	require.NoError(t, err)
	var names []string
	for _, c := range hc.Commands() {
		names = append(names, c.Name())
	}
	assert.ElementsMatch(t, []string{"run", "audit", "apply", "rollback", "list"}, names)
}

func TestApplyEnvFillsUnsetFlags(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_URL", "https://cp.example")
	t.Setenv("NEBULACTRL_ENROLL_TOKEN", "tok-123")
	t.Setenv("NEBULACTRL_PROXY_HTTP_PORT", "8080")
	t.Setenv("NEBULACTRL_INSECURE_DEV", "true")
	t.Setenv("NEBULACTRL_UPTIME_INTERVAL", "30s")

	cmd := newCommand()
	require.NoError(t, applyEnv(cmd.Flags()))

	get := func(n string) string { return cmd.Flags().Lookup(n).Value.String() }
	assert.Equal(t, "https://cp.example", get("url"))
	assert.Equal(t, "tok-123", get("token"))
	assert.Equal(t, "8080", get("proxy-http-port"))
	assert.Equal(t, "true", get("insecure-dev"))
	assert.Equal(t, "30s", get("uptime-interval"))
	assert.Equal(t, "/var/lib/nebulactrl-agent", get("state-dir"), "unset variable keeps the default")
}

func TestApplyEnvCommandLineWinsAndEmptyIsIgnored(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_URL", "https://from-env")
	t.Setenv("NEBULACTRL_STATE_DIR", "") // empty must not override the default

	cmd := newCommand()
	require.NoError(t, cmd.Flags().Parse([]string{"--url", "https://from-flag"}))
	require.NoError(t, applyEnv(cmd.Flags()))
	assert.Equal(t, "https://from-flag", cmd.Flags().Lookup("url").Value.String())
	assert.Equal(t, "/var/lib/nebulactrl-agent", cmd.Flags().Lookup("state-dir").Value.String())
}

func TestApplyEnvInvalidValueNamesTheVariable(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_PROXY_HTTP_PORT", "eighty")
	t.Setenv("NEBULACTRL_PROXY_HTTPS_PORT", "alsobad")

	err := applyEnv(newCommand().Flags())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NEBULACTRL_PROXY_HTTP", "the first failing variable is reported")
	assert.Contains(t, err.Error(), "eighty")
}

func TestApplyEnvSkipsHelpAndVersion(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_HELP", "true")
	t.Setenv("NEBULACTRL_VERSION", "true")
	cmd := newCommand()
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	require.NoError(t, applyEnv(cmd.Flags()))
	assert.Equal(t, "false", cmd.Flags().Lookup("help").Value.String())
	assert.Equal(t, "false", cmd.Flags().Lookup("version").Value.String())
}

func TestHostname(t *testing.T) {
	t.Setenv("HOSTNAME", "abc123def")
	assert.Equal(t, "abc123def", hostname())

	t.Setenv("HOSTNAME", "")
	want, err := os.Hostname()
	require.NoError(t, err)
	assert.Equal(t, want, hostname(), "falls back to the OS hostname")
}

func TestNewLoggerLevelFromEnvironment(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		env                      string
		debug, info, warn, errOn bool
	}{
		{"", false, true, true, true},
		{"debug", true, true, true, true},
		{"WARN", false, false, true, true},
		{"error", false, false, false, true},
		{"not-a-level", false, true, true, true},
	} {
		t.Run("level="+tc.env, func(t *testing.T) {
			t.Setenv("NEBULACTRL_LOG_LEVEL", tc.env)
			l := newLogger()
			assert.Equal(t, tc.debug, l.Enabled(ctx, slog.LevelDebug))
			assert.Equal(t, tc.info, l.Enabled(ctx, slog.LevelInfo))
			assert.Equal(t, tc.warn, l.Enabled(ctx, slog.LevelWarn))
			assert.Equal(t, tc.errOn, l.Enabled(ctx, slog.LevelError))
		})
	}
}

func TestNewLoggerTagsTheService(t *testing.T) {
	t.Setenv("NEBULACTRL_LOG_LEVEL", "")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	l := newLogger()
	l.Info("hello")
	os.Stderr = orig
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"service":"nebulactrl-agent"`)
	assert.Contains(t, string(out), `"msg":"hello"`)
}

// execAgent runs the root command with args and returns the error RunE gave.
func execAgent(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newCommand()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(context.Background())
}

func TestRunRejectsBadURLs(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	for name, args := range map[string][]string{
		"missing url":                {},
		"unsupported scheme":         {"--url", "ftp://cp.example"},
		"plaintext without insecure": {"--url", "http://cp.example"},
	} {
		t.Run(name, func(t *testing.T) {
			err := execAgent(t, args...)
			require.Error(t, err)
		})
	}
}

func TestRunReadsTheURLFromTheEnvironment(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	t.Setenv("NEBULACTRL_URL", "ftp://from-env.example")
	err := execAgent(t)
	require.Error(t, err, "the env var reached run(), which rejected the scheme")
	assert.NotContains(t, err.Error(), "NEBULACTRL_URL")
}

func TestRunReportsABadEnvironmentValueBeforeRunning(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	t.Setenv("NEBULACTRL_URL", "https://cp.example")
	t.Setenv("NEBULACTRL_PROXY_HTTP_PORT", "eighty")
	err := execAgent(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NEBULACTRL_PROXY_HTTP_PORT")
}

func TestRunRejectsTooShortUptimeInterval(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	err := execAgent(t, "--url", "https://cp.example", "--uptime-interval", "500ms")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--uptime-interval")
	assert.Contains(t, err.Error(), "at least 1s")
	assert.Contains(t, err.Error(), "500ms")
}

func TestRunRejectsBadNetworkPool(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	err := execAgent(t, "--url", "https://cp.example", "--network-pool", "not-a-cidr")
	require.Error(t, err)
}

func TestRunFailsWhenDockerIsUnreachable(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	t.Setenv("DOCKER_HOST", "unix:///nonexistent-nebulactrl/docker.sock")
	dir := t.TempDir()
	err := execAgent(t, "--url", "https://cp.example", "--state-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker daemon unreachable")
	_, statErr := os.Stat(filepath.Join(dir, store.FileName))
	assert.True(t, os.IsNotExist(statErr), "the state store is opened only after Docker answered")
}

func TestVersionFlagPrintsTheVersion(t *testing.T) {
	cmd := newCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), version)
}

// captureHandler records log records.
type captureHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.recs {
		var attrs []string
		r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a.Key+"="+a.Value.String()); return true })
		out = append(out, r.Message+" "+strings.Join(attrs, " "))
	}
	return out
}

func TestPruneCommandsLoopPrunesOldRecordsOnce(t *testing.T) {
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.MarkCommand("fresh"))
	require.NoError(t, st.MarkCommand("unacked"))
	require.NoError(t, st.PutResult(agentprotocol.ResultFrame{CommandID: "unacked", Outcome: agentprotocol.OutcomeSucceeded}))

	// Records are stamped "now", so a 14-day cutoff keeps them all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the loop prunes once, then sees the cancelled context
	h := &captureHandler{}
	done := make(chan struct{})
	go func() { pruneCommandsLoop(ctx, slog.New(h), st); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pruneCommandsLoop ignores a cancelled context")
	}
	_, seen := st.SeenCommand("fresh")
	assert.True(t, seen, "a record younger than 14 days is kept")
	_, seen = st.SeenCommand("unacked")
	assert.True(t, seen)
	assert.Empty(t, h.messages(), "nothing pruned, nothing logged")
}

func TestPruneCommandsLoopLogsAFailingStore(t *testing.T) {
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, st.Close()) // every call on a closed store fails

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &captureHandler{}
	pruneCommandsLoop(ctx, slog.New(h), st)
	msgs := h.messages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0], "prune commands")
	assert.Contains(t, msgs[0], "err=")
}
