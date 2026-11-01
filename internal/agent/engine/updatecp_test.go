package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/internal/releasesign/releasesigntest"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scriptRunner is a host that answers every script with out and remembers it.
type scriptRunner struct {
	out     hardening.Output
	scripts []string
}

func (r *scriptRunner) Mode() string { return hardening.ModeHost }

func (r *scriptRunner) Run(_ context.Context, script string) (hardening.Output, error) {
	r.scripts = append(r.scripts, script)
	return r.out, nil
}

// testInstaller is the installer the stubbed verification returns.
const testInstaller = "#!/usr/bin/env bash\nNC_INSTALLER_VERSION=1\n"

func updateEngine(t *testing.T, out hardening.Output) (*Engine, *scriptRunner) {
	t.Helper()
	r := &scriptRunner{out: out}
	prev := newHardeningRunner
	newHardeningRunner = func(context.Context, *Engine) (hardening.Runner, error) { return r, nil }
	prevVerify := verifyInstaller
	verifyInstaller = func(context.Context, string) ([]byte, error) { return []byte(testInstaller), nil }
	t.Cleanup(func() { newHardeningRunner = prev; verifyInstaller = prevVerify })
	e, err := New(Config{
		Docker: newFakeDocker(), Proxy: &fakeProxy{}, Sender: &fakeSender{},
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
	})
	require.NoError(t, err)
	return e, r
}

func updateCmd(t *testing.T, p agentprotocol.UpdateControlPlanePayload) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-u", Kind: agentprotocol.KindUpdateControlPlane, Payload: raw}
}

var goodUpdate = agentprotocol.UpdateControlPlanePayload{Version: "0.1.2", InstallerURL: "https://nebulactrl.com/install.sh"}

func TestUpdateControlPlaneStartsTheInstallerDetached(t *testing.T) {
	e, r := updateEngine(t, hardening.Output{Stdout: "@@OK nebulactrl-update-1767225600\n"})
	res := e.HandleCommand(context.Background(), updateCmd(t, goodUpdate))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.UpdateControlPlaneResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, "nebulactrl-update-1767225600", out.Unit)
	require.Contains(t, out.Message, "journalctl -u nebulactrl-update-1767225600")

	require.Len(t, r.scripts, 1)
	s := r.scripts[0]
	sum := sha256.Sum256([]byte(testInstaller))
	require.True(t, strings.HasPrefix(s, "VERSION='0.1.2'\nURL='https://nebulactrl.com/install.sh'\nSHA256='"+hex.EncodeToString(sum[:])+"'\n"), s)
	require.Contains(t, s, `[ "$SUM" = "$SHA256" ]`, "the host runs only the file the agent verified")
	require.Contains(t, s, "--proto '=https'", "the download itself is https only")
	require.Contains(t, s, "systemd-run", "the installer must outlive the agent and the container")
	require.Contains(t, s, "--collect")
	require.Contains(t, s, `bash "$0" --update-to "$1"`, "the installer gets the target version without pinning it")
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindUpdateControlPlane])
}

func TestUpdateControlPlaneReportsWhyItDidNotStart(t *testing.T) {
	e, _ := updateEngine(t, hardening.Output{Stdout: "@@ERR this host does not run systemd, so the installer cannot be started as a detached unit\n"})
	res := e.HandleCommand(context.Background(), updateCmd(t, goodUpdate))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "does not run systemd")

	// A script that ends without a verdict is a failure that names the exit and stderr.
	e, _ = updateEngine(t, hardening.Output{ExitCode: 127, Stderr: "sh: boom"})
	res = e.HandleCommand(context.Background(), updateCmd(t, goodUpdate))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "status 127")
	require.Contains(t, res.Error, "sh: boom")
}

func TestUpdateControlPlaneRejectsBadPayloadsBeforeTouchingTheHost(t *testing.T) {
	e, r := updateEngine(t, hardening.Output{Stdout: "@@OK x\n"})
	for name, p := range map[string]agentprotocol.UpdateControlPlanePayload{
		"version with a shell":   {Version: "0.1.2; reboot", InstallerURL: goodUpdate.InstallerURL},
		"not a release":          {Version: "latest", InstallerURL: goodUpdate.InstallerURL},
		"file url":               {Version: "0.1.2", InstallerURL: "file:///etc/passwd"},
		"quote in url":           {Version: "0.1.2", InstallerURL: "https://x.example/a'b"},
		"command substitution":   {Version: "0.1.2", InstallerURL: "https://x.example/$(reboot)"},
		"no url":                 {Version: "0.1.2"},
		"newline in url":         {Version: "0.1.2", InstallerURL: "https://x.example/a\nreboot"},
		"plain http":             {Version: "0.1.2", InstallerURL: "http://nebulactrl.com/install.sh"},
		"credentials in the url": {Version: "0.1.2", InstallerURL: "https://user:token@nebulactrl.com/install.sh"},
		"pre-release is allowed": {Version: "0.2.0-rc.1", InstallerURL: goodUpdate.InstallerURL},
	} {
		res := e.HandleCommand(context.Background(), updateCmd(t, p))
		if name == "pre-release is allowed" {
			require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, name)
			continue
		}
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, name)
		require.Contains(t, res.Error, "invalid update_control_plane payload", name)
	}
	require.Len(t, r.scripts, 1, "only the valid pre-release reached the host")

	bad := agentprotocol.Command{ID: "x", Kind: agentprotocol.KindUpdateControlPlane, Payload: json.RawMessage(`{`)}
	require.Equal(t, agentprotocol.OutcomeFailed, e.HandleCommand(context.Background(), bad).Outcome)
}

func TestUpdateControlPlaneRefusesAnInstallerWithoutAValidSignature(t *testing.T) {
	e, r := updateEngine(t, hardening.Output{Stdout: "@@OK x\n"})
	verifyInstaller = func(context.Context, string) ([]byte, error) {
		return nil, releasesign.ErrInvalid
	}
	res := e.HandleCommand(context.Background(), updateCmd(t, goodUpdate))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "release signature")
	require.Empty(t, r.scripts, "an installer that does not verify never reaches the host")
}

func TestUpdateControlPlaneVerifiesTheInstallerWithTheSignedFile(t *testing.T) {
	signer, pub := releasesigntest.NewSigner()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/install.sh":
			_, _ = w.Write([]byte(testInstaller))
		case "/install.sh.minisig":
			_, _ = w.Write(signer.Sign([]byte("a different installer\n"), "install.sh"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	e, r := updateEngine(t, hardening.Output{Stdout: "@@OK x\n"})
	verifyInstaller = func(ctx context.Context, url string) ([]byte, error) {
		return releasesign.FetchVerified(ctx, srv.Client(), url, pub, maxInstallerBytes)
	}
	res := e.HandleCommand(context.Background(), updateCmd(t, agentprotocol.UpdateControlPlanePayload{Version: "0.1.2", InstallerURL: srv.URL + "/install.sh"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, "the installer differs from the file the signature covers")
	require.Empty(t, r.scripts)
}
