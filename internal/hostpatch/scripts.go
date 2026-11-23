// Package hostpatch holds the pieces of server patching that both the agent and
// the control plane need: the POSIX shell programs that check a host for pending
// updates, upgrade it and schedule its reboot, the parsers of what they print,
// and the list of packages whose upgrade deserves a warning. It has no
// dependencies besides the agent protocol types.
//
// Everything runs on the host through the same runner host hardening uses (the
// agent itself, or a privileged helper container that enters PID 1's
// namespaces). The upgrade runs detached from that runner, as a transient
// systemd unit, because upgrading Docker restarts the daemon and with it the
// helper container (and the agent when it runs in a container); a detached
// upgrade finishes anyway and the agent picks its result up afterwards.
package hostpatch

import (
	"fmt"
	"regexp"
)

// StateDir is where the host keeps the scripts, logs and exit codes of
// upgrades, so a restarted agent can pick a running upgrade up again.
const StateDir = "/var/lib/nebulactrl-patching"

// LogTailBytes is how much of an upgrade's output is kept.
const LogTailBytes = 48 * 1024

const preamble = `LC_ALL=C
export LC_ALL
PATH="$PATH:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export PATH
`

// CheckScript refreshes the package lists, lists the pending upgrades and
// reports whether the host asks for a reboot. It changes no package. Output is
// a sequence of "@@NAME value" lines and sections, ended by "@@END" (see
// ParseCheck).
const CheckScript = preamble + `
if command -v timeout >/dev/null 2>&1; then _t="timeout 300"; else _t=""; fi
echo "@@BOOTID $(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
if command -v apt-get >/dev/null 2>&1; then
  echo "@@MANAGER apt"
  DEBIAN_FRONTEND=noninteractive $_t apt-get -o Acquire::Retries=3 update -qq >/dev/null 2>&1
  echo "@@REFRESH $?"
  echo "@@UPDATES"
  apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst '
  echo "@@UPDATESRC 0"
  _nv="$(apt-config dump 2>/dev/null | sed -n 's/^APT::Periodic::Unattended-Upgrade "\(.*\)";.*/\1/p' | head -n1)"
  if command -v unattended-upgrade >/dev/null 2>&1 && [ -n "$_nv" ] && [ "$_nv" != "0" ]; then echo "@@NATIVE unattended-upgrades"; else echo "@@NATIVE"; fi
  if [ -e /run/reboot-required ] || [ -e /var/run/reboot-required ]; then
    echo "@@REBOOT 1"
    echo "@@REBOOTPKGS"
    cat /run/reboot-required.pkgs /var/run/reboot-required.pkgs 2>/dev/null
  else
    echo "@@REBOOT 0"
  fi
elif command -v dnf >/dev/null 2>&1; then
  echo "@@MANAGER dnf"
  echo "@@UPDATES"
  $_t dnf -q check-update --refresh 2>/dev/null
  echo "@@UPDATESRC $?"
  echo "@@SECURITY"
  $_t dnf -q check-update --security 2>/dev/null
  echo "@@SECURITYRC $?"
  _r=0
  if command -v needs-restarting >/dev/null 2>&1; then
    needs-restarting -r >/dev/null 2>&1
    [ $? -eq 1 ] && _r=1
  else
    _run="$(uname -r)"
    _new="$(rpm -q --last kernel-core kernel 2>/dev/null | head -n1 | awk '{print $1}' | sed -e 's/^kernel-core-//' -e 's/^kernel-//')"
    [ -n "$_new" ] && [ "$_new" != "$_run" ] && _r=1
  fi
  echo "@@REBOOT $_r"
  _nat=""
  if command -v systemctl >/dev/null 2>&1; then
    for _u in dnf-automatic.timer dnf-automatic-install.timer dnf5-automatic.timer; do
      case "$(systemctl is-enabled "$_u" 2>/dev/null)" in enabled|enabled-runtime) _nat="dnf-automatic" ;; esac
    done
  fi
  echo "@@NATIVE $_nat"
else
  echo "@@MANAGER none"
fi
echo "@@END"
exit 0
`

// RebootScript schedules a reboot a few seconds ahead, so the command can still
// answer, and prints the boot id it will replace. It prefers a transient systemd
// timer (independent of whatever runs this script), then shutdown(8).
const RebootScript = preamble + `
echo "@@BOOTID $(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
if command -v systemd-run >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  if systemd-run --quiet --collect --unit="nebulactrl-reboot-$(date +%s)" --on-active=5s systemctl reboot >/dev/null 2>&1; then
    echo "@@SCHEDULED systemd"; exit 0
  fi
fi
if command -v shutdown >/dev/null 2>&1; then
  if shutdown -r +1 "NebulaCtrl: reboot requested" >/dev/null 2>&1; then
    echo "@@SCHEDULED shutdown"; exit 0
  fi
fi
echo "@@FAILED no way to schedule a reboot (needs systemd-run or shutdown)"
exit 0
`

var runIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

func validRunID(id string) error {
	if !runIDPattern.MatchString(id) {
		return fmt.Errorf("invalid run id %q", id)
	}
	return nil
}

// UpgradeOptions shapes the upgrade program.
type UpgradeOptions struct {
	// SecurityOnly upgrades only the packages with a security update.
	SecurityOnly bool
	// Scheduled is the nightly run of automatic updates: it first checks the free
	// disk space (exit status ExitSkipped when there is too little), leaves the
	// built-in holds and Holds alone, and lets apt install new dependencies of
	// kept-back packages (kernel meta packages). A manual apply upgrades exactly
	// what the person reviewed and is unaffected by holds.
	Scheduled bool
	// Holds are extra package names a scheduled upgrade leaves alone.
	Holds []string
}

// ExitSkipped is the exit status of an upgrade that did not start because the
// host failed the preflight; the log's last "nebulactrl: skipped: ..." line says why.
const ExitSkipped = 75

// SkipMarker starts the line the preflight prints when it skips the upgrade.
const SkipMarker = "nebulactrl: skipped: "

// MinVarFreeKiB and MinBootFreeKiB are the free space a scheduled upgrade needs
// on /var (package downloads) and on a separate /boot (kernel and initramfs).
const (
	MinVarFreeKiB  = 1024 * 1024
	MinBootFreeKiB = 300 * 1024
)

// preflightScript skips a scheduled upgrade on a nearly full disk.
var preflightScript = fmt.Sprintf(`_kb() { df -Pk "$1" 2>/dev/null | awk 'NR==2 {print $4}'; }
_v="$(_kb /var)"
if [ -n "$_v" ] && [ "$_v" -lt %[1]d ] 2>/dev/null; then echo "%[3]sless than 1 GiB free on /var"; exit %[4]d; fi
if awk '$2 == "/boot" {f=1} END {exit !f}' /proc/mounts 2>/dev/null; then
  _b="$(_kb /boot)"
  if [ -n "$_b" ] && [ "$_b" -lt %[2]d ] 2>/dev/null; then echo "%[3]sless than 300 MiB free on /boot"; exit %[4]d; fi
fi
`, MinVarFreeKiB, MinBootFreeKiB, SkipMarker, ExitSkipped)

// upgradeScript returns the program that upgrades the packages.
func upgradeScript(manager string, o UpgradeOptions) (string, error) {
	holds, err := holdPatterns(o.Holds)
	if err != nil {
		return "", err
	}
	switch manager {
	case "apt":
		const refresh = `apt-get -o Acquire::Retries=3 -o DPkg::Lock::Timeout=600 update -qq || echo "could not refresh the package lists; using the last ones"
`
		const install = `apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600`
		if o.Scheduled {
			filter := ""
			if o.SecurityOnly {
				filter = " | grep -e '-security'"
			}
			// Only upgrades of installed packages ("Inst name [old] (new ...)") are
			// listed: "install --only-upgrade" would skip a brand-new package, which
			// comes in as a dependency of the one that needs it. The held packages are
			// dropped from the list instead of being "apt-mark hold"-ed, so a failure
			// cannot leave a stray hold behind.
			return preamble + `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1
` + preflightScript + `held() { case "$1" in ` + holds + `) return 0 ;; esac; return 1; }
` + refresh + `_all="$(apt-get -s -o Debug::NoLocking=1 --with-new-pkgs upgrade 2>/dev/null | grep '^Inst [^ ]* \[' ` + filter + ` | awk '{print $2}')"
_pkgs=""; _held=""
for _p in $_all; do if held "$_p"; then _held="$_held $_p"; else _pkgs="$_pkgs $_p"; fi; done
[ -z "$_held" ] || echo "Held back:$_held"
if [ -z "$_pkgs" ]; then echo "Nothing to upgrade."; exit 0; fi
echo "Upgrading:$_pkgs"
for _p in $(apt-get -s -o Debug::NoLocking=1 install --only-upgrade $_pkgs 2>/dev/null | grep '^Inst ' | awk '{print $2}'); do
  if held "$_p"; then echo "The upgrade would also change the held package $_p. Upgrade it from the Updates tab instead."; exit 1; fi
done
` + install + ` install --only-upgrade $_pkgs
`, nil
		}
		if o.SecurityOnly {
			return preamble + `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1
` + refresh + `_pkgs="$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst ' | grep -e '-security' | awk '{print $2}')"
if [ -z "$_pkgs" ]; then echo "No security updates are pending."; exit 0; fi
echo "Upgrading: $_pkgs"
` + install + ` install --only-upgrade $_pkgs
`, nil
		}
		return preamble + `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1
` + refresh + install + ` upgrade
`, nil
	case "dnf":
		sec := ""
		if o.SecurityOnly {
			sec = " --security"
		}
		if o.Scheduled {
			ex, err := excludeList(o.Holds)
			if err != nil {
				return "", err
			}
			return preamble + preflightScript + "dnf -y upgrade" + sec + " --exclude='" + ex + "'\n", nil
		}
		return preamble + "dnf -y upgrade" + sec + "\n", nil
	}
	return "", fmt.Errorf("unsupported package manager %q", manager)
}

// StartScript starts the upgrade of run runID, detached from the runner. It is
// safe to run twice: it prints "@@STARTED" the first time and "@@RUNNING" or
// "@@DONE" when the run already started, so an agent that restarted attaches to
// the upgrade instead of starting another.
func StartScript(runID, manager string, o UpgradeOptions) (string, error) {
	if err := validRunID(runID); err != nil {
		return "", err
	}
	up, err := upgradeScript(manager, o)
	if err != nil {
		return "", err
	}
	return preamble + fmt.Sprintf(`D=%[1]s
ID=%[2]s
umask 077
mkdir -p "$D" || { echo "@@FAILED cannot create $D"; exit 0; }
find "$D" -type f ! -name 'native-*' -mtime +7 -delete 2>/dev/null
if [ -e "$D/$ID.rc" ]; then echo "@@DONE"; exit 0; fi
if [ -e "$D/$ID.started" ]; then echo "@@RUNNING"; exit 0; fi
: > "$D/$ID.started"
cat > "$D/$ID.sh" <<'NEBULACTRL_EOF'
%[3]sNEBULACTRL_EOF
RUNNER="sh $D/$ID.sh > $D/$ID.log 2>&1; echo \$? > $D/$ID.rc"
if command -v systemd-run >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  if systemd-run --quiet --collect --no-block --unit="nebulactrl-patch-$ID" sh -c "$RUNNER" >/dev/null 2>&1; then
    echo "unit nebulactrl-patch-$ID" > "$D/$ID.how"
    echo "@@STARTED"; exit 0
  fi
fi
nohup setsid sh -c "$RUNNER" >/dev/null 2>&1 &
echo "pid $!" > "$D/$ID.how"
echo "@@STARTED"
exit 0
`, StateDir, runID, up), nil
}

// StatusScript reports where the upgrade of run runID stands: "@@RC <n>" and
// the log tail when it finished, "@@RUNNING" while it runs, "@@LOST" when its
// process is gone without leaving an exit code (the host rebooted mid-upgrade),
// "@@NONE" when no such run exists.
func StatusScript(runID string) (string, error) {
	if err := validRunID(runID); err != nil {
		return "", err
	}
	return preamble + fmt.Sprintf(`D=%[1]s
ID=%[2]s
if [ -e "$D/$ID.rc" ]; then
  echo "@@RC $(cat "$D/$ID.rc")"
  echo "@@LOG"
  tail -c %[3]d "$D/$ID.log" 2>/dev/null
  exit 0
fi
if [ ! -e "$D/$ID.started" ]; then echo "@@NONE"; exit 0; fi
how="$(cat "$D/$ID.how" 2>/dev/null)"
case "$how" in
  unit\ *) if systemctl is-active --quiet "${how#unit }" 2>/dev/null; then alive=1; else alive=0; fi ;;
  pid\ *)  if kill -0 "${how#pid }" 2>/dev/null; then alive=1; else alive=0; fi ;;
  *)       alive=1 ;;
esac
if [ "$alive" = 1 ]; then
  echo "@@RUNNING"
  echo "@@LOG"
  tail -c 4096 "$D/$ID.log" 2>/dev/null
else
  echo "@@LOST"
  echo "@@LOG"
  tail -c %[3]d "$D/$ID.log" 2>/dev/null
fi
exit 0
`, StateDir, runID, LogTailBytes), nil
}
