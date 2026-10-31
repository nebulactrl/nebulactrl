package lynis

import (
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The remediations are POSIX shell, because they run on the host itself (often
// through an nsenter helper that has only the host's own tools) and because the
// same text is shown to a person as the manual command. Every fix has four
// parts:
//
//   - Precondition: exit 1 (with the reason on stdout) when the fix cannot apply
//     to this host (a container, no systemd, too little memory ...).
//   - Check: exit 0 when the host already satisfies the fix.
//   - Apply: make the change idempotently, keeping what it needs to undo it.
//   - Rollback: undo exactly what Apply did.
//
// The driver (Script) runs Apply in a subshell with `set -e`, verifies with
// Check afterwards, and rolls the fix back by itself when the verification
// fails, so a fix is either in place or not at all.
//
// NC_ROOT prefixes every path (/proc/sys, /etc, /var/lib) so the unit tests run
// the real scripts against a scratch tree.

// prelude defines the helpers the fixes use.
const prelude = `
R="${NC_ROOT:-}"
S="$R/var/lib/nebulactrl/hardening"
NC_TMP="${TMPDIR:-/tmp}"
PATH="$PATH:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export PATH
LC_ALL=C
export LC_ALL

nc_note() { printf '%s\n' "$*"; }
nc_skip() { printf '@@SKIP %s\n' "$*"; exit 0; }
nc_state() { mkdir -p "$S/backup" "$S/applied"; }
nc_msg() { printf '%s\n' "$1" | awk 'NF && !/^@@/ {gsub(/\r/, ""); printf "%s%s", (n++ ? "; " : ""), $0}' | cut -c1-300; }
nc_tidy() { # remove state files that hold nothing any more
  for _x in kv.prev chmod.prev sysctl.prev sysctl.skip; do [ -f "$S/$_x" ] && [ ! -s "$S/$_x" ] && rm -f "$S/$_x"; done
  rmdir "$S/applied" "$S/backup" "$S" 2>/dev/null
  return 0
}

# ---- sysctl: values are written to /proc/sys directly, like sysctl(8) does ----
nc_path() { printf '%s/proc/sys/%s' "$R" "$(printf '%s' "$1" | tr . /)"; }
nc_prev_has() { awk -F= -v k="$2" '$1==k {f=1} END{exit !f}' "$1" 2>/dev/null; }
# nc_sysctl_ok KEY WANT eq|min: the key is absent, cannot be set here, or has the wanted value (min: at least).
nc_sysctl_ok() {
  _p=$(nc_path "$1")
  [ -r "$_p" ] || return 0
  if [ -f "$S/sysctl.skip" ] && grep -qxF "$1" "$S/sysctl.skip"; then return 0; fi
  _c=$(cat "$_p" 2>/dev/null) || return 0
  if [ "$3" = min ]; then [ "${_c%% *}" -ge "$2" ] 2>/dev/null; else [ "$_c" = "$2" ]; fi
}
nc_conf_begin() { NC_CONF_FILE="$1"; NC_CONF=$(mktemp "$NC_TMP/ncconf.XXXXXX") || exit 1; }
nc_conf_commit() {
  if [ -s "$NC_CONF" ]; then
    mkdir -p "$(dirname "$NC_CONF_FILE")" || exit 1
    _m=$(mktemp "$NC_TMP/ncconf.XXXXXX") || exit 1
    { [ -f "$NC_CONF_FILE" ] && grep -v '^#' "$NC_CONF_FILE"; cat "$NC_CONF"; } |
      awk '{k=$1; if (!(k in o)) {o[k]=++n; ord[n]=k} v[k]=$0} END{for (i=1;i<=n;i++) print v[ord[i]]}' > "$_m"
    { printf '# Managed by NebulaCtrl host hardening. Delete this file to undo it.\n'; cat "$_m"; } > "$NC_CONF_FILE"
    chmod 644 "$NC_CONF_FILE"
    rm -f "$_m"
  fi
  rm -f "$NC_CONF"
}
# nc_sysctl_set KEY WANT eq|min: set the live value, remember the old one, queue it for the drop-in.
nc_sysctl_set() {
  _p=$(nc_path "$1")
  if [ ! -e "$_p" ]; then nc_note "$1: not present on this kernel, left alone"; return 0; fi
  nc_sysctl_ok "$1" "$2" "$3" && return 0
  _c=$(cat "$_p")
  nc_prev_has "$S/sysctl.prev" "$1" || printf '%s=%s\n' "$1" "$_c" >> "$S/sysctl.prev"
  if printf '%s\n' "$2" > "$_p" 2>/dev/null; then
    nc_note "$1 = $2 (was $_c)"
    printf '%s = %s\n' "$1" "$2" >> "$NC_CONF"
  else
    nc_note "$1: cannot be changed from here, left alone"
    printf '%s\n' "$1" >> "$S/sysctl.skip"
  fi
}
nc_sysctl_restore() {
  _p=$(nc_path "$1")
  if [ -f "$S/sysctl.prev" ] && nc_prev_has "$S/sysctl.prev" "$1"; then
    _v=$(awk -F= -v k="$1" '$1==k {print substr($0, length(k)+2); exit}' "$S/sysctl.prev")
    if [ -w "$_p" ] && printf '%s\n' "$_v" > "$_p" 2>/dev/null; then nc_note "$1 = $_v (restored)"; fi
    awk -F= -v k="$1" '$1!=k' "$S/sysctl.prev" > "$S/sysctl.prev.new" && cat "$S/sysctl.prev.new" > "$S/sysctl.prev"; rm -f "$S/sysctl.prev.new"
  fi
  if [ -f "$S/sysctl.skip" ]; then
    grep -vxF "$1" "$S/sysctl.skip" > "$S/sysctl.skip.new"; cat "$S/sysctl.skip.new" > "$S/sysctl.skip"; rm -f "$S/sysctl.skip.new"
  fi
}

# ---- key/value files such as /etc/login.defs ----
nc_kv_get() { awk -v k="$2" '$1==k {v=$2} END{print v}' "$1"; }
# nc_kv_set FILE KEY VALUE: set (or add) KEY, remembering what it was.
nc_kv_set() {
  _f=$1; _k=$2; _v=$3
  [ -f "$_f" ] || return 0
  _old=$(nc_kv_get "$_f" "$_k")
  [ "$_old" = "$_v" ] && return 0
  if ! awk -F'|' -v f="$_f" -v k="$_k" '$1==f && $2==k {x=1} END{exit !x}' "$S/kv.prev" 2>/dev/null; then
    _line=$(awk -v k="$_k" '$1==k {l=$0} END{print l}' "$_f")
    if [ -n "$_line" ]; then printf '%s|%s|%s\n' "$_f" "$_k" "$_line" >> "$S/kv.prev"
    else printf '%s|%s|@@absent\n' "$_f" "$_k" >> "$S/kv.prev"; fi
  fi
  _t=$(mktemp "$NC_TMP/nckv.XXXXXX") || return 1
  if awk -v k="$_k" '$1==k {x=1} END{exit !x}' "$_f"; then
    awk -v k="$_k" -v v="$_v" '$1==k {print k "\t" v; next} {print}' "$_f" > "$_t"
  else
    cat "$_f" > "$_t"; printf '%s\t%s\n' "$_k" "$_v" >> "$_t"
  fi
  cat "$_t" > "$_f"; rm -f "$_t"
  nc_note "$_k $_v in ${_f#"$R"}"
}
nc_kv_restore() {
  _f=$1; _k=$2
  [ -f "$S/kv.prev" ] || return 0
  _old=$(awk -F'|' -v f="$_f" -v k="$_k" '$1==f && $2==k {print substr($0, length(f)+length(k)+3); exit}' "$S/kv.prev")
  [ -n "$_old" ] || return 0
  _t=$(mktemp "$NC_TMP/nckv.XXXXXX") || return 1
  if [ "$_old" = "@@absent" ]; then
    awk -v k="$_k" '$1!=k' "$_f" > "$_t"
  else
    NC_LINE="$_old" awk -v k="$_k" '$1==k && !d {print ENVIRON["NC_LINE"]; d=1; next} $1==k {next} {print}' "$_f" > "$_t"
  fi
  cat "$_t" > "$_f"; rm -f "$_t"
  awk -F'|' -v f="$_f" -v k="$_k" '!($1==f && $2==k)' "$S/kv.prev" > "$S/kv.prev.new"; cat "$S/kv.prev.new" > "$S/kv.prev"; rm -f "$S/kv.prev.new"
  nc_note "$_k restored in ${_f#"$R"}"
}

nc_mode() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
# ---- permissions: only ever tighten ----
nc_chmod() {
  [ -e "$2" ] || return 0
  _cur=$(nc_mode "$2")
  [ $(( 0$_cur & ~0$1 & 07777 )) -eq 0 ] && return 0
  awk -F'|' -v p="$2" '$2==p {x=1} END{exit !x}' "$S/chmod.prev" 2>/dev/null || printf '%s|%s\n' "$_cur" "$2" >> "$S/chmod.prev"
  chmod "$1" "$2" && nc_note "${2#"$R"}: $_cur -> $1"
}
nc_chmod_ok() { [ -e "$2" ] || return 0; [ $(( 0$(nc_mode "$2") & ~0$1 & 07777 )) -eq 0 ]; }
nc_chmod_restore() {
  [ -f "$S/chmod.prev" ] || return 0
  _m=$(awk -F'|' -v p="$1" '$2==p {print $1; exit}' "$S/chmod.prev")
  [ -n "$_m" ] || return 0
  [ -e "$1" ] && chmod "$_m" "$1" && nc_note "${1#"$R"}: back to $_m"
  awk -F'|' -v p="$1" '$2!=p' "$S/chmod.prev" > "$S/chmod.prev.new"; cat "$S/chmod.prev.new" > "$S/chmod.prev"; rm -f "$S/chmod.prev.new"
}

# ---- kernel modules: block loading through modprobe.d ----
nc_mod_loaded() { _n=$(printf '%s' "$1" | tr - _); awk -v m="$_n" '$1==m {x=1} END{exit !x}' "$R/proc/modules" 2>/dev/null; }
# nc_mod_ok FILE MODULE: the module is blocked in FILE, or in use right now (left alone).
nc_mod_ok() { nc_mod_loaded "$2" && return 0; grep -qxF "install $2 /bin/false" "$1" 2>/dev/null; }
# nc_mod_block FILE MODULE...
nc_mod_block() {
  _f=$1; shift
  _t=$(mktemp "$NC_TMP/ncmod.XXXXXX") || exit 1
  _w=""
  for _m in "$@"; do
    if nc_mod_loaded "$_m"; then nc_note "$_m: loaded right now, left alone"; continue; fi
    printf 'install %s /bin/false\nblacklist %s\n' "$_m" "$_m" >> "$_t"
    _w="$_w $_m"
  done
  if [ -s "$_t" ]; then
    mkdir -p "$(dirname "$_f")" || exit 1
    { printf '# Managed by NebulaCtrl host hardening. Delete this file to undo it.\n'; cat "$_t"; } > "$_f"
    chmod 644 "$_f"
    nc_note "blocked:$_w"
  fi
  rm -f "$_t"
}

# ---- drivers: one line of machine-readable output per fix ----
nc_status() { # id pre chk
  _o=$( ( "$3" ) 2>&1 ); _rc=$?
  if [ $_rc -eq 0 ]; then
    if [ -f "$S/applied/$1" ]; then _st=applied; else _st=compliant; fi
    printf '@@FIX %s %s %s\n' "$1" "$_st" "$(nc_msg "$_o")"
    return 0
  fi
  _p=$( ( "$2" ) 2>&1 ); _prc=$?
  if [ $_prc -ne 0 ]; then printf '@@FIX %s unavailable %s\n' "$1" "$(nc_msg "$_p")"
  else printf '@@FIX %s pending\n' "$1"; fi
}
nc_apply() { # id pre chk apply rollback
  _id=$1
  if ( "$3" ) >/dev/null 2>&1; then printf '@@OUT %s unchanged\n' "$_id"; return 0; fi
  if ! _p=$( ( "$2" ) 2>&1 ); then printf '@@OUT %s skipped %s\n' "$_id" "$(nc_msg "$_p")"; return 0; fi
  if ! nc_state 2>/dev/null; then printf '@@OUT %s failed cannot write %s\n' "$_id" "${S#"$R"}"; return 0; fi
  _o=$( ( set -e; "$4" ) 2>&1 ); _rc=$?
  if [ $_rc -ne 0 ]; then
    ( "$5" ) >/dev/null 2>&1
    printf '@@OUT %s failed %s\n' "$_id" "$(nc_msg "$_o")"
    return 0
  fi
  case "$_o" in *'@@SKIP '*)
    printf '@@OUT %s skipped %s\n' "$_id" "$(printf '%s\n' "$_o" | sed -n 's/^@@SKIP //p' | head -n1)"
    return 0 ;;
  esac
  if ( "$3" ) >/dev/null 2>&1; then
    : > "$S/applied/$_id"
    case "$_o" in *'@@REBOOT'*) printf '@@REBOOT %s\n' "$_id" ;; esac
    printf '@@OUT %s applied %s\n' "$_id" "$(nc_msg "$_o")"
  else
    ( "$5" ) >/dev/null 2>&1
    printf '@@OUT %s failed did not take effect, rolled back\n' "$_id"
  fi
}
nc_rollback() { # id rollback
  _id=$1
  if [ ! -f "$S/applied/$_id" ]; then printf '@@OUT %s skipped NebulaCtrl did not apply this fix\n' "$_id"; return 0; fi
  if _o=$( ( set -e; "$2" ) 2>&1 ); then
    rm -f "$S/applied/$_id"
    nc_tidy
    printf '@@OUT %s rolled_back %s\n' "$_id" "$(nc_msg "$_o")"
  else
    printf '@@OUT %s failed %s\n' "$_id" "$(nc_msg "$_o")"
  fi
}
`

// Script actions.
const (
	// ScriptStatus prints the state of every given fix.
	ScriptStatus = "status"
	// ScriptApply applies the given fixes.
	ScriptApply = "apply"
	// ScriptRollback undoes the given fixes.
	ScriptRollback = "rollback"
)

// fnName turns a fix ID into a shell function suffix.
func fnName(id string) string { return strings.NewReplacer("-", "_").Replace(id) }

// Script builds the shell program that performs action for fixes. The output
// has one `@@FIX id state message` line per fix for ScriptStatus, and
// `@@OUT id outcome message` lines (plus `@@REBOOT id`) for the others.
func Script(action string, fixes []Fix) string {
	var b strings.Builder
	b.WriteString(prelude)
	for _, f := range fixes {
		n := fnName(f.ID)
		pre := f.Precondition
		if strings.TrimSpace(pre) == "" {
			pre = ":"
		}
		fmt.Fprintf(&b, "nc_pre_%s() {\n%s\n}\nnc_chk_%s() {\n%s\n}\nnc_app_%s() {\n%s\n}\nnc_rbk_%s() {\n%s\n}\n",
			n, pre, n, f.Check, n, f.Apply, n, f.Rollback)
	}
	for _, f := range fixes {
		n := fnName(f.ID)
		switch action {
		case ScriptStatus:
			fmt.Fprintf(&b, "nc_status %s nc_pre_%s nc_chk_%s\n", shq(f.ID), n, n)
		case ScriptApply:
			fmt.Fprintf(&b, "nc_apply %s nc_pre_%s nc_chk_%s nc_app_%s nc_rbk_%s\n", shq(f.ID), n, n, n, n)
		case ScriptRollback:
			fmt.Fprintf(&b, "nc_rollback %s nc_rbk_%s\n", shq(f.ID), n)
		}
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// shq single-quotes s for a POSIX shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ParseDriverOutput reads what Script printed. States are filled for
// ScriptStatus, outcomes for ScriptApply and ScriptRollback.
func ParseDriverOutput(out string) (states []agentprotocol.HardeningFixState, outcomes []agentprotocol.HardeningFixOutcome, reboot bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "@@FIX "):
			id, rest, _ := strings.Cut(strings.TrimPrefix(line, "@@FIX "), " ")
			state, msg, _ := strings.Cut(rest, " ")
			if id != "" && state != "" {
				states = append(states, agentprotocol.HardeningFixState{ID: id, State: state, Message: strings.TrimSpace(msg)})
			}
		case strings.HasPrefix(line, "@@OUT "):
			id, rest, _ := strings.Cut(strings.TrimPrefix(line, "@@OUT "), " ")
			outcome, msg, _ := strings.Cut(rest, " ")
			if id != "" && outcome != "" {
				outcomes = append(outcomes, agentprotocol.HardeningFixOutcome{ID: id, Outcome: outcome, Message: strings.TrimSpace(msg)})
			}
		case strings.HasPrefix(line, "@@REBOOT "):
			reboot = true
		}
	}
	return states, outcomes, reboot
}
