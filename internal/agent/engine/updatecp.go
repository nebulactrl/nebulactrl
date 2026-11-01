package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// updateControlPlaneTimeout bounds the start of the installer (the download and
// systemd-run); the installer itself runs on, detached, for as long as it needs.
const updateControlPlaneTimeout = 5 * time.Minute

// updateLockKey serializes update_control_plane commands; no service has this ID.
const updateLockKey = "\x00update-control-plane"

// maxInstallerBytes bounds the installer download (the assembled script is about 200 KiB).
const maxInstallerBytes = 4 << 20

// verifyInstaller downloads the installer from url together with its minisign
// signature (url + ".minisig") and returns the installer only when the signature
// verifies under the release key compiled into this agent. A variable so tests
// can serve a signed installer from a local TLS server.
var verifyInstaller = func(ctx context.Context, url string) ([]byte, error) {
	return releasesign.FetchVerified(ctx, nil, url, releasesign.PublicKey, maxInstallerBytes)
}

// releaseVersion is what the installer accepts after --update-to for a release.
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)

// handleUpdateControlPlane starts the installer on this host, detached from the
// agent and from the control plane's container: a transient systemd unit, so
// the installer survives both being restarted by the upgrade it performs. The
// installer runs as root, so the agent first verifies its signature against the
// release key compiled into the agent; the host script then runs only a file with
// the SHA-256 of what was verified. It reports only that the installer started or
// why it could not; the control plane observes the outcome when the new version
// comes up.
func (e *Engine) handleUpdateControlPlane(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.UpdateControlPlanePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid update_control_plane payload: "+err.Error())
	}
	if err := validateUpdatePayload(p); err != nil {
		return failedResult(cmd.ID, "invalid update_control_plane payload: "+err.Error())
	}
	uctx, cancel := context.WithTimeout(ctx, updateControlPlaneTimeout)
	defer cancel()
	installer, err := verifyInstaller(uctx, p.InstallerURL)
	if err != nil {
		e.log.Warn("control plane update refused: the installer is not signed by the release key", "version", p.Version, "err", err)
		return failedResult(cmd.ID, "the installer was not run: it could not be verified against the NebulaCtrl release signature ("+err.Error()+"). Update with the command shown in Admin → Updates, or check that the feed's origin serves install.sh and install.sh.minisig from the release")
	}
	sum := sha256.Sum256(installer)
	runner, err := newHardeningRunner(ctx, e)
	if err != nil {
		return failedResult(cmd.ID, "control plane update: "+err.Error())
	}
	unlock := e.lockService(updateLockKey)
	defer unlock()

	out, err := runner.Run(uctx, updateControlPlaneScript(p, hex.EncodeToString(sum[:])))
	if err != nil {
		return failedResult(cmd.ID, "control plane update: "+err.Error())
	}
	unit, reason := parseUpdateOutput(out.Stdout)
	if unit == "" {
		if reason == "" {
			reason = fmt.Sprintf("the host script ended with status %d without a result: %s", out.ExitCode, strings.TrimSpace(out.Stderr))
		}
		e.log.Warn("control plane update did not start", "version", p.Version, "reason", reason)
		return failedResult(cmd.ID, reason)
	}
	e.log.Info("control plane update started", "version", p.Version, "unit", unit, "mode", runner.Mode())
	data, _ := json.Marshal(agentprotocol.UpdateControlPlaneResult{
		Unit: unit, Message: "The installer runs as the systemd unit " + unit + " (journalctl -u " + unit + ").",
	})
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

func validateUpdatePayload(p agentprotocol.UpdateControlPlanePayload) error {
	if !releaseVersion.MatchString(p.Version) {
		return fmt.Errorf("version %q is not a release version", p.Version)
	}
	if err := releasesign.ValidateSourceURL(p.InstallerURL); err != nil {
		return fmt.Errorf("installerUrl: %w", err)
	}
	for _, r := range p.InstallerURL {
		if r <= ' ' || r == 0x7f || r == '\'' || r == '\\' || r == '`' || r == '$' {
			return errors.New("installerUrl has characters a shell would interpret")
		}
	}
	return nil
}

// parseUpdateOutput reads the host script's verdict: "@@OK <unit>" or "@@ERR <why>".
func parseUpdateOutput(stdout string) (unit, reason string) {
	for _, l := range strings.Split(stdout, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "@@OK "):
			return strings.TrimSpace(strings.TrimPrefix(l, "@@OK ")), ""
		case strings.HasPrefix(l, "@@ERR "):
			return "", strings.TrimSpace(strings.TrimPrefix(l, "@@ERR "))
		}
	}
	return "", ""
}

// updateControlPlaneScript is the POSIX shell program that runs on the host.
// Version and URL are validated and single-quoted, the SHA-256 is hex the agent
// computed; nothing else is interpolated.
func updateControlPlaneScript(p agentprotocol.UpdateControlPlanePayload, sha256sum string) string {
	return "VERSION=" + shellQuote(p.Version) + "\nURL=" + shellQuote(p.InstallerURL) + "\nSHA256=" + shellQuote(sha256sum) + "\n" + updateControlPlaneBody
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// updateControlPlaneBody downloads the installer, checks it is the file the agent
// verified (SHA-256) and starts it as a transient unit. It always exits 0 and says
// what happened on a marker line.
const updateControlPlaneBody = `
DIR=/opt/nebulactrl
STAGE=/var/lib/nebulactrl-update
fail() { echo "@@ERR $1"; exit 0; }
[ -d /run/systemd/system ] && command -v systemd-run >/dev/null 2>&1 || fail "this host does not run systemd, so the installer cannot be started as a detached unit"
[ -r "$DIR/.env" ] || fail "no NebulaCtrl installation was found in $DIR on this host (installed with another --dir? then update it with the command shown in Admin → Updates)"
command -v curl >/dev/null 2>&1 || fail "curl is not installed on this host"
if systemctl list-units --no-legend --plain --state=active,activating 'nebulactrl-update-*' 2>/dev/null | grep -q 'nebulactrl-update-'; then
  fail "another update is still running on this host (journalctl -u 'nebulactrl-update-*')"
fi
mkdir -p "$STAGE" && chmod 0700 "$STAGE" || fail "cannot create $STAGE"
TS=$(date +%s)
FILE="$STAGE/install-$TS.sh"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 120 -o "$FILE" "$URL" || { rm -f "$FILE"; fail "could not download the installer from $URL"; }
SUM=$(sha256sum "$FILE" 2>/dev/null | cut -d' ' -f1)
[ -n "$SUM" ] || SUM=$(shasum -a 256 "$FILE" 2>/dev/null | cut -d' ' -f1)
[ -n "$SUM" ] || { rm -f "$FILE"; fail "sha256sum is not installed on this host, so the installer cannot be checked"; }
[ "$SUM" = "$SHA256" ] || { rm -f "$FILE"; fail "what $URL served is not the installer the release signature covers"; }
head -n 1 "$FILE" | grep -q '^#!.*bash' && grep -q '^NC_INSTALLER_VERSION=' "$FILE" || { rm -f "$FILE"; fail "what $URL served is not the NebulaCtrl installer"; }
chmod 0700 "$FILE"
UNIT="nebulactrl-update-$TS"
out=$(systemd-run --quiet --unit="$UNIT" --collect --description="NebulaCtrl update to $VERSION" --setenv=HOME=/root \
  /bin/bash -c 'bash "$0" --update-to "$1"; rc=$?; rm -f "$0"; exit "$rc"' "$FILE" "$VERSION" 2>&1) || { rm -f "$FILE"; fail "systemd-run could not start the installer: $out"; }
echo "@@OK $UNIT"
`
