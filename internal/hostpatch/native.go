package hostpatch

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// AptAutoUpdatesDropIn is the apt configuration file that switches the host's
// own unattended-upgrades off while NebulaCtrl runs the nightly updates. It sorts
// after the stock 20auto-upgrades, so its value wins.
const AptAutoUpdatesDropIn = "/etc/apt/apt.conf.d/99nebulactrl-auto-updates"

// NativeTimersFile remembers which dnf automatic timers NebulaCtrl disabled, so
// that restoring enables only those.
const NativeTimersFile = StateDir + "/native-timers"

// nativeTimerPattern is the shell case pattern a timer name from NativeTimersFile
// must match before systemctl sees it.
const nativeTimerPattern = `dnf-automatic*.timer|dnf5-automatic*.timer`

// NativeScript switches the host's own automatic updates off
// (agentprotocol.PatchNativeDisable) or back on (PatchNativeRestore). apt hosts
// get a drop-in with APT::Periodic::Unattended-Upgrade "0"; dnf hosts get the
// automatic timers disabled, remembered in NativeTimersFile. Both are idempotent.
// Output: "@@APPLIED <directive>" or "@@FAILED <reason>" (see ParseNative).
func NativeScript(directive string) (string, error) {
	var body string
	switch directive {
	case agentprotocol.PatchNativeDisable:
		body = `if command -v apt-get >/dev/null 2>&1; then
  mkdir -p "$(dirname "$F")" || fail "cannot create $(dirname "$F")"
  printf '%s\n' '// Managed by NebulaCtrl, which applies the updates on its own schedule. Remove with the automatic updates setting.' 'APT::Periodic::Unattended-Upgrade "0";' > "$F.tmp" && mv "$F.tmp" "$F" || fail "cannot write $F"
elif command -v systemctl >/dev/null 2>&1; then
  for u in dnf-automatic.timer dnf-automatic-install.timer dnf5-automatic.timer; do
    case "$(systemctl is-enabled "$u" 2>/dev/null)" in
      enabled|enabled-runtime)
        mkdir -p "$D" || fail "cannot create $D"
        grep -qx "$u" "$T" 2>/dev/null || echo "$u" >> "$T" || fail "cannot write $T"
        systemctl disable --now "$u" >/dev/null 2>&1 || fail "cannot disable $u" ;;
    esac
  done
fi
`
	case agentprotocol.PatchNativeRestore:
		body = `rm -f "$F" "$F.tmp" || fail "cannot remove $F"
if [ -s "$T" ]; then
  while read -r u; do
    case "$u" in
      ` + nativeTimerPattern + `) systemctl enable --now "$u" >/dev/null 2>&1 || fail "cannot enable $u" ;;
    esac
  done < "$T"
  rm -f "$T"
fi
`
	default:
		return "", fmt.Errorf("unknown native directive %q", directive)
	}
	return preamble + fmt.Sprintf(`F=%[1]s
D=%[2]s
T=%[3]s
fail() { echo "@@FAILED $1"; exit 0; }
%[4]secho "@@APPLIED %[5]s"
exit 0
`, AptAutoUpdatesDropIn, StateDir, NativeTimersFile, body, directive), nil
}

// ParseNative reads the output of NativeScript and returns the directive it applied.
func ParseNative(out string) (string, error) {
	s := parseSections(out)
	if s.has("FAILED") {
		return "", errors.New(s.values["FAILED"])
	}
	if !s.has("APPLIED") {
		return "", errors.New("the host did not confirm the change")
	}
	return s.values["APPLIED"], nil
}

// ContainersScript lists the running containers with their restart policy, for
// the decision to reboot a server: containers come back after a reboot only with
// the policy "always" or "unless-stopped". Output: "@@DOCKER ok" with one
// "name policy" line per container under "@@CONTAINERS", or "@@DOCKER <reason>"
// when the host's Docker could not be asked; ended by "@@END" (see ParseContainers).
const ContainersScript = preamble + `
if command -v timeout >/dev/null 2>&1; then _t="timeout 60"; else _t=""; fi
if ! command -v docker >/dev/null 2>&1; then echo "@@DOCKER the docker command is not available on the host"; echo "@@END"; exit 0; fi
if ! _ids="$($_t docker ps -q 2>/dev/null)"; then echo "@@DOCKER the Docker daemon did not answer"; echo "@@END"; exit 0; fi
if [ -n "$_ids" ]; then
  if ! _list="$($_t docker inspect --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}}' $_ids 2>/dev/null)"; then
    echo "@@DOCKER the containers could not be inspected"; echo "@@END"; exit 0
  fi
fi
echo "@@DOCKER ok"
echo "@@CONTAINERS"
[ -z "$_list" ] || echo "$_list"
echo "@@END"
exit 0
`

// maxUnsafeContainers bounds PatchContainers.Unsafe.
const maxUnsafeContainers = 20

// ParseContainers reads the output of ContainersScript.
func ParseContainers(out string) agentprotocol.PatchContainers {
	s := parseSections(out)
	if !s.has("END") {
		return agentprotocol.PatchContainers{Reason: "the host's answer was cut short"}
	}
	if v := s.values["DOCKER"]; v != "ok" {
		if v == "" {
			v = "Docker did not answer"
		}
		return agentprotocol.PatchContainers{Reason: v}
	}
	res := agentprotocol.PatchContainers{Known: true}
	for _, line := range s.lines["CONTAINERS"] {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		res.Running++
		policy := ""
		if len(f) > 1 {
			policy = f[1]
		}
		if policy != "always" && policy != "unless-stopped" && len(res.Unsafe) < maxUnsafeContainers {
			res.Unsafe = append(res.Unsafe, strings.TrimPrefix(f[0], "/"))
		}
	}
	return res
}
