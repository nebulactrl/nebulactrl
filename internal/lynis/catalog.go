package lynis

import (
	"fmt"
	"slices"
	"strings"
)

// Fix is one curated, safe remediation of a Lynis finding.
//
// The catalog is deliberately short. A fix is in it only when it is reversible,
// changes nothing a service or a login depends on in normal use, and cannot cut
// off the administrator. Each Lynis test ID below links to CISOfy's page for the
// control (https://cisofy.com/lynis/controls/<ID>/).
//
// Never in the catalog, whatever Lynis suggests: anything about SSH (sshd
// options, host keys, file modes under /etc/ssh), the firewall, user accounts
// and their passwords, kernel.modules_disabled (one-way, and Docker loads
// modules), net.ipv4.ip_forward (Docker routes through it) and strict
// rp_filter (it breaks asymmetric routes, Tailscale and WireGuard policy
// routing). TestCatalogNeverTouches enforces the first two.
type Fix struct {
	// ID is the stable slug of the fix.
	ID    string
	Title string
	// Why says what the fix changes and why that is better, in a sentence or two.
	Why string
	// Risk is the honest downside, shown before the fix is applied.
	Risk string
	// Tests are the Lynis test IDs the fix answers.
	Tests []string
	// Keys are the sysctl keys the fix sets (for KRNL-6000, whose finding lists
	// one detail per key).
	Keys []string
	// Default fixes are applied by "Apply safe fixes" and by the installer.
	// The others are offered one by one.
	Default bool
	// Reboot says the fix only takes effect after the next reboot.
	Reboot bool

	// Precondition exits 1, printing the reason, when the fix cannot apply here.
	Precondition string
	// Check exits 0 when the host already satisfies the fix; what it prints is a note.
	Check string
	// Apply changes the host; Rollback restores exactly what Apply changed.
	Apply    string
	Rollback string
	// Manual is what a person types to do the same by hand.
	Manual []string
}

// sysctlSpec is one key of a sysctl fix.
type sysctlSpec struct {
	Key  string
	Want string
	// Min means "at least Want" (a stricter value is kept); otherwise exact.
	Min bool
}

func (s sysctlSpec) mode() string {
	if s.Min {
		return "min"
	}
	return "eq"
}

func sysctlFix(id, title, why, risk string, def bool, tests []string, specs ...sysctlSpec) Fix {
	file := "$R/etc/sysctl.d/90-nebulactrl-" + id + ".conf"
	var chk, app, rbk strings.Builder
	var keys []string
	var manual []string
	manual = append(manual, fmt.Sprintf("sudo tee /etc/sysctl.d/90-nebulactrl-%s.conf <<'EOF'", id))
	fmt.Fprintf(&app, "nc_state\nnc_conf_begin \"%s\"\n", file)
	for _, s := range specs {
		keys = append(keys, s.Key)
		fmt.Fprintf(&chk, "nc_sysctl_ok %s %s %s || exit 1\n", shq(s.Key), shq(s.Want), s.mode())
		fmt.Fprintf(&app, "nc_sysctl_set %s %s %s\n", shq(s.Key), shq(s.Want), s.mode())
		fmt.Fprintf(&rbk, "nc_sysctl_restore %s\n", shq(s.Key))
		manual = append(manual, fmt.Sprintf("%s = %s", s.Key, s.Want))
	}
	app.WriteString("nc_conf_commit\n")
	fmt.Fprintf(&rbk, "rm -f \"%s\"\n", file)
	manual = append(manual, "EOF", "sudo sysctl --system")
	return Fix{
		ID: id, Title: title, Why: why, Risk: risk, Tests: tests, Keys: keys, Default: def,
		Check: chk.String(), Apply: app.String(), Rollback: rbk.String(), Manual: manual,
	}
}

func modprobeFix(id, title, why, risk string, def bool, tests []string, mods ...string) Fix {
	file := "$R/etc/modprobe.d/nebulactrl-" + id + ".conf"
	var chk strings.Builder
	for _, m := range mods {
		fmt.Fprintf(&chk, "nc_mod_ok \"%s\" %s || exit 1\n", file, shq(m))
	}
	manual := []string{fmt.Sprintf("sudo tee /etc/modprobe.d/nebulactrl-%s.conf <<'EOF'", id)}
	for _, m := range mods {
		manual = append(manual, "install "+m+" /bin/false", "blacklist "+m)
	}
	manual = append(manual, "EOF")
	return Fix{
		ID: id, Title: title, Why: why, Risk: risk, Tests: tests, Default: def,
		Check:    chk.String(),
		Apply:    fmt.Sprintf("nc_state\nnc_mod_block %q %s\n", file, shqAll(mods)),
		Rollback: fmt.Sprintf("rm -f %q\n", file),
		Manual:   manual,
	}
}

func shqAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = shq(s)
	}
	return strings.Join(q, " ")
}

const (
	cronMode  = "600"
	cronDir   = "700"
	issueText = "Authorized access only. This system is monitored and audited.\\nUnauthorized access or use is prohibited and may result in legal action and criminal penalties under applicable law.\\n"
)

// cronPaths are the cron files and directories of Lynis's FILE-7524 permission
// profile that NebulaCtrl tightens. The allow and deny lists are left alone:
// on Debian and Ubuntu crontab(1) runs setgid and reads them through its group.
var (
	cronFiles = []string{"/etc/crontab"}
	cronDirs  = []string{"/etc/cron.d", "/etc/cron.hourly", "/etc/cron.daily", "/etc/cron.weekly", "/etc/cron.monthly"}
)

func cronFix() Fix {
	var chk, app, rbk strings.Builder
	manual := []string{}
	add := func(mode, p string) {
		fmt.Fprintf(&chk, "nc_chmod_ok %s \"$R%s\" || exit 1\n", mode, p)
		fmt.Fprintf(&app, "nc_chmod %s \"$R%s\"\n", mode, p)
		fmt.Fprintf(&rbk, "nc_chmod_restore \"$R%s\"\n", p)
		manual = append(manual, fmt.Sprintf("[ -e %s ] && sudo chmod %s %s", p, mode, p))
	}
	for _, p := range cronFiles {
		add(cronMode, p)
	}
	for _, p := range cronDirs {
		add(cronDir, p)
	}
	return Fix{
		ID:    "cron-permissions",
		Title: "Restrict cron files to root",
		Why:   "/etc/crontab and the /etc/cron.* directories are readable by every local user, which shows them which jobs run as root and with which arguments. Mode 600 (files) and 700 (directories) is what Lynis expects.",
		Risk:  "Local users other than root can no longer read the system crontabs. cron itself runs as root and is not affected. The cron.allow, cron.deny, at.allow and at.deny lists are not touched.",
		Tests: []string{"FILE-7524"}, Default: true,
		Check: chk.String(), Apply: "nc_state\n" + app.String(), Rollback: rbk.String(), Manual: manual,
	}
}

func bannerFix() Fix {
	// Lynis counts legal key words (access, authori, monitor, audit, prohib, legal,
	// criminal, penal, law ...) and wants five of them.
	count := `nc_banner_ok() { [ -f "$1" ] || return 1; _n=0; for _w in access authori monitor audit prohib legal criminal penal; do grep -qi "$_w" "$1" && _n=$((_n+1)); done; [ "$_n" -ge 5 ]; }
`
	const fn = `nc_banner_write() { # file: put the banner above the existing text
  _f=$1
  nc_banner_ok "$_f" && return 0
  _b="$S/backup/$(printf '%s' "$_f" | tr / _)"
  if [ ! -e "$_b" ] && [ ! -e "$_b.absent" ]; then if [ -e "$_f" ]; then cp -p "$_f" "$_b"; else : > "$_b.absent"; fi; fi
  _t=$(mktemp "$NC_TMP/ncban.XXXXXX") || exit 1
  printf '` + issueText + `\n' > "$_t"
  [ -f "$_f" ] && cat "$_f" >> "$_t"
  cat "$_t" > "$_f"; chmod 644 "$_f"
  cp -p "$_f" "$_b.written"
  rm -f "$_t"
  nc_note "banner added to ${_f#"$R"}"
}
nc_banner_undo() { # file: restore the original unless someone edited the file since
  _f=$1; _b="$S/backup/$(printf '%s' "$_f" | tr / _)"
  [ -e "$_b.written" ] || return 0
  if cmp -s "$_f" "$_b.written"; then
    if [ -e "$_b" ]; then cat "$_b" > "$_f"; rm -f "$_b"; else rm -f "$_f"; rm -f "$_b.absent"; fi
    nc_note "${_f#"$R"} restored"
  else
    nc_note "${_f#"$R"} was edited since; left as it is"
    rm -f "$_b" "$_b.absent"
  fi
  rm -f "$_b.written"
}
`
	return Fix{
		ID:    "legal-banner",
		Title: "Add a legal banner to /etc/issue and /etc/issue.net",
		Why:   "A warning shown before login removes the excuse that access was not known to be restricted. The banner is put above the existing text, which stays.",
		Risk:  "Cosmetic: the text appears on the console login prompt. It is shown over SSH only if sshd's Banner option points at /etc/issue.net, which NebulaCtrl never sets.",
		Tests: []string{"BANN-7126", "BANN-7130"}, Default: true,
		Check: count + `nc_banner_ok "$R/etc/issue" && nc_banner_ok "$R/etc/issue.net"`,
		Apply: count + fn + "nc_state\nnc_banner_write \"$R/etc/issue\"\nnc_banner_write \"$R/etc/issue.net\"\n",
		Rollback: count + fn +
			"nc_banner_undo \"$R/etc/issue\"\nnc_banner_undo \"$R/etc/issue.net\"\n",
		Manual: []string{"printf '" + issueText + "' | sudo tee -a /etc/issue /etc/issue.net"},
	}
}

// loginDefsKey is one /etc/login.defs setting: it is set to Value only when
// Need (a shell test on the current value $v) succeeds, so a stricter value of
// the administrator is kept.
type loginDefsKey struct{ Key, Value, Need string }

func loginDefsFix(id, title, why, risk string, tests []string, keys ...loginDefsKey) Fix {
	var chk, app, rbk strings.Builder
	var manual []string
	app.WriteString("nc_state\n")
	for _, k := range keys {
		get := fmt.Sprintf("v=$(nc_kv_get \"$R/etc/login.defs\" %s)\n", shq(k.Key))
		fmt.Fprintf(&chk, "%sif %s; then exit 1; fi\n", get, k.Need)
		fmt.Fprintf(&app, "%sif %s; then nc_kv_set \"$R/etc/login.defs\" %s %s; fi\n", get, k.Need, shq(k.Key), shq(k.Value))
		fmt.Fprintf(&rbk, "nc_kv_restore \"$R/etc/login.defs\" %s\n", shq(k.Key))
		manual = append(manual, fmt.Sprintf("grep -q '^%s[[:space:]]' /etc/login.defs && sudo sed -i -E 's/^%s[[:space:]].*/%s\\t%s/' /etc/login.defs || printf '%s\\t%s\\n' | sudo tee -a /etc/login.defs", k.Key, k.Key, k.Key, k.Value, k.Key, k.Value))
	}
	return Fix{
		ID: id, Title: title, Why: why, Risk: risk, Tests: tests, Default: true,
		Precondition: `[ -f "$R/etc/login.defs" ] || { echo "/etc/login.defs does not exist"; exit 1; }`,
		Check:        chk.String(), Apply: app.String(), Rollback: rbk.String(), Manual: manual,
	}
}

// Catalog is the curated set of safe remediations, in the order they are applied.
func Catalog() []Fix {
	fixes := []Fix{
		sysctlFix("sysctl-kernel", "Harden kernel information leaks and debugging",
			"Hides kernel addresses and the kernel log from unprivileged users, disables the magic SysRq key and Ctrl-Alt-Del reboot, restricts ptrace to a process's own children, stops line disciplines from loading on demand and hardens the BPF JIT.",
			"kptr_restrict=2 also hides kernel symbol addresses from root-run profilers such as perf and bpftrace. ptrace_scope=1 stops non-root users from attaching a debugger (strace -p, gdb -p) to processes that are not their children; root can still. The SysRq key stops working on the console.",
			true, []string{"KRNL-6000"},
			sysctlSpec{"kernel.dmesg_restrict", "1", true},
			sysctlSpec{"kernel.kptr_restrict", "2", true},
			sysctlSpec{"kernel.sysrq", "0", false},
			sysctlSpec{"kernel.ctrl-alt-del", "0", false},
			sysctlSpec{"kernel.yama.ptrace_scope", "1", true},
			sysctlSpec{"dev.tty.ldisc_autoload", "0", false},
			sysctlSpec{"net.core.bpf_jit_harden", "2", true},
			sysctlSpec{"kernel.core_uses_pid", "1", false},
		),
		sysctlFix("sysctl-fs", "Disable core dumps of setuid programs and protect links",
			"Stops setuid programs from dumping core (a dump can contain secrets) and blocks the classic hard-link and symlink races in world-writable directories.",
			"None known in normal use. Setuid programs no longer leave core files for debugging.",
			true, []string{"KRNL-6000", "KRNL-5820"},
			sysctlSpec{"fs.suid_dumpable", "0", false},
			sysctlSpec{"fs.protected_hardlinks", "1", true},
			sysctlSpec{"fs.protected_symlinks", "1", true},
		),
		sysctlFix("sysctl-net", "Harden the network stack against spoofing and redirects",
			"Ignores ICMP redirects and source-routed packets, does not send redirects, logs impossible (martian) source addresses, answers SYN floods with SYN cookies and ignores broadcast pings. Forwarding and reverse-path filtering are not touched: Docker needs the first and Tailscale or multi-homed hosts need the second loose.",
			"log_martians writes a kernel log line for each spoofed packet, which is noisy on a host exposed to the internet (the kernel rate-limits it). Hosts that rely on ICMP redirects or source routing, which is rare, lose them.",
			true, []string{"KRNL-6000"},
			sysctlSpec{"net.ipv4.conf.all.accept_redirects", "0", false},
			sysctlSpec{"net.ipv4.conf.default.accept_redirects", "0", false},
			sysctlSpec{"net.ipv6.conf.all.accept_redirects", "0", false},
			sysctlSpec{"net.ipv6.conf.default.accept_redirects", "0", false},
			sysctlSpec{"net.ipv4.conf.all.send_redirects", "0", false},
			sysctlSpec{"net.ipv4.conf.default.send_redirects", "0", false},
			sysctlSpec{"net.ipv4.conf.all.accept_source_route", "0", false},
			sysctlSpec{"net.ipv4.conf.default.accept_source_route", "0", false},
			sysctlSpec{"net.ipv6.conf.all.accept_source_route", "0", false},
			sysctlSpec{"net.ipv6.conf.default.accept_source_route", "0", false},
			sysctlSpec{"net.ipv4.conf.all.log_martians", "1", true},
			sysctlSpec{"net.ipv4.conf.default.log_martians", "1", true},
			sysctlSpec{"net.ipv4.tcp_syncookies", "1", true},
			sysctlSpec{"net.ipv4.icmp_echo_ignore_broadcasts", "1", true},
			sysctlSpec{"net.ipv4.icmp_ignore_bogus_error_responses", "1", true},
		),
		sysctlFix("sysctl-fs-protected", "Restrict FIFO and regular file creation in sticky directories",
			"Stops a program from opening a FIFO or regular file that another user planted in /tmp or another sticky directory, which defeats a class of privilege-escalation tricks.",
			"A root job that opens a lock file in /tmp created by another user (cron jobs sharing a lock path, some installers) now fails with permission denied. Audit your jobs before enabling it.",
			false, []string{"KRNL-6000"},
			sysctlSpec{"fs.protected_fifos", "2", true},
			sysctlSpec{"fs.protected_regular", "2", true},
		),
		{
			ID:    "core-dumps",
			Title: "Turn off core dumps",
			Why:   "A core dump is a copy of a crashed process's memory, secrets included, written where other tools can read it. This sets systemd-coredump to store nothing and the PAM core size limit of login sessions to 0.",
			Risk:  "Crashes leave no core file or systemd-coredump entry for post-mortem debugging. Container processes are not affected by the PAM limit, and dockerd's own limit is unchanged.",
			Tests: []string{"KRNL-5820"}, Default: true,
			Check: `grep -qs '^Storage=none' "$R/etc/systemd/coredump.conf.d/nebulactrl-hardening.conf" && grep -qs '^\*[[:space:]]*hard[[:space:]]*core[[:space:]]*0' "$R/etc/security/limits.d/nebulactrl-hardening.conf"`,
			Apply: `nc_state
mkdir -p "$R/etc/systemd/coredump.conf.d" "$R/etc/security/limits.d"
printf '# Managed by NebulaCtrl host hardening. Delete this file to undo it.\n[Coredump]\nStorage=none\nProcessSizeMax=0\n' > "$R/etc/systemd/coredump.conf.d/nebulactrl-hardening.conf"
printf '# Managed by NebulaCtrl host hardening. Delete this file to undo it.\n* hard core 0\n' > "$R/etc/security/limits.d/nebulactrl-hardening.conf"
chmod 644 "$R/etc/systemd/coredump.conf.d/nebulactrl-hardening.conf" "$R/etc/security/limits.d/nebulactrl-hardening.conf"
nc_note "core dumps disabled for new sessions and crashes"`,
			Rollback: `rm -f "$R/etc/systemd/coredump.conf.d/nebulactrl-hardening.conf" "$R/etc/security/limits.d/nebulactrl-hardening.conf"`,
			Manual: []string{
				"printf '[Coredump]\\nStorage=none\\nProcessSizeMax=0\\n' | sudo tee /etc/systemd/coredump.conf.d/nebulactrl-hardening.conf",
				"echo '* hard core 0' | sudo tee /etc/security/limits.d/nebulactrl-hardening.conf",
			},
		},
		loginDefsFix("umask", "Set the default umask to 027",
			"New files made in login sessions are no longer readable by every local user: group read, no access for others.",
			"Files you create by hand (also with sudo, because pam_umask applies to it) are mode 640/750. A service running as another user can no longer read a config file you wrote with sudo tee unless you chmod it. Services started by systemd are not affected: they use their own UMask.",
			[]string{"AUTH-9328"},
			loginDefsKey{"UMASK", "027", `case "$v" in 027|0027|077|0077) false;; *) true;; esac`}),
		loginDefsFix("password-ageing", "Set password ageing defaults for new accounts",
			"Sets a maximum password age of 365 days, a minimum of 1 day and the SHA-crypt round limits in /etc/login.defs. These are defaults for accounts created from now on: existing accounts, root and system accounts are never changed (no chage), so no key-only login or service account can start asking for a new password.",
			"New human accounts must change their password once a year and cannot change it twice in one day. Values you already set stricter than ours are kept.",
			[]string{"AUTH-9286", "AUTH-9230"},
			loginDefsKey{"PASS_MAX_DAYS", "365", `[ -z "$v" ] || [ "$v" = 99999 ]`},
			loginDefsKey{"PASS_MIN_DAYS", "1", `[ -z "$v" ] || ! [ "$v" -ge 1 ] 2>/dev/null`},
			loginDefsKey{"SHA_CRYPT_MIN_ROUNDS", "5000", `[ -z "$v" ] || ! [ "$v" -ge 5000 ] 2>/dev/null`},
			loginDefsKey{"SHA_CRYPT_MAX_ROUNDS", "10000", `[ -z "$v" ] || ! [ "$v" -ge 5000 ] 2>/dev/null`}),
		cronFix(),
		modprobeFix("protocols", "Block rarely used network protocols",
			"DCCP, SCTP, RDS and TIPC are almost never needed on a server and each has had kernel vulnerabilities. Blocking their modules removes that attack surface.",
			"Software that really uses one of them (telephony stacks with SCTP, some cluster filesystems with TIPC or RDS) stops working. A module that is loaded right now is left alone.",
			true, []string{"NETW-3200"}, "dccp", "sctp", "rds", "tipc"),
		modprobeFix("firewire", "Block FireWire drivers",
			"FireWire can read host memory by DMA. A server has no use for it.",
			"None unless a FireWire device is attached. A loaded module is left alone.",
			true, []string{"STRG-1846"}, "firewire-core", "firewire-ohci", "ohci1394"),
		modprobeFix("filesystems", "Block unused filesystem drivers",
			"cramfs, freevxfs, jffs2, hfs and hfsplus are rare filesystems whose drivers are an attack surface when a crafted image is mounted. squashfs (used by snap) and udf (used by some cloud providers' config drives) are deliberately not blocked.",
			"A host that mounts one of those filesystems stops being able to. A loaded module is left alone.",
			true, []string{"FILE-6430"}, "cramfs", "freevxfs", "jffs2", "hfs", "hfsplus"),
		bannerFix(),
		modprobeFix("usb-storage", "Block USB storage",
			"Stops a USB drive from being mounted, a route for data theft and for malware on a physical host.",
			"A USB disk, including a backup drive, stops working until you undo the fix. Only for hosts nobody plugs drives into.",
			false, []string{"USB-1000"}, "usb-storage"),
		{
			ID:    "tmp-mount",
			Title: "Mount /tmp as a separate tmpfs with nosuid and nodev",
			Why:   "A separate /tmp cannot fill the root filesystem and, with nosuid and nodev, cannot be used to plant a setuid program or a device node. The change is made with systemd's own tmp.mount unit and applies at the next boot.",
			Risk:  "/tmp becomes RAM-backed (up to half of memory by default) and is emptied at every boot. Programs that leave large files in /tmp can run the host out of memory. noexec is deliberately not set because package scripts need to run from /tmp.",
			Tests: []string{"FILE-6310"}, Default: false, Reboot: true,
			Precondition: `[ "$(cat /proc/1/comm 2>/dev/null)" = systemd ] || { echo "this host does not run systemd"; exit 1; }
if command -v systemd-detect-virt >/dev/null 2>&1 && systemd-detect-virt -cq; then echo "this host is a container"; exit 1; fi
[ -e /usr/lib/systemd/system/tmp.mount ] || [ -e /lib/systemd/system/tmp.mount ] || { echo "systemd's tmp.mount unit is not installed"; exit 1; }
_mem=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
[ "${_mem:-0}" -ge 2000000 ] || { echo "less than 2 GB of memory: a RAM-backed /tmp could starve builds"; exit 1; }`,
			Check: `_o=$(findmnt -no OPTIONS /tmp 2>/dev/null || true)
case "$_o" in *nosuid*nodev*|*nodev*nosuid*) exit 0;; esac
if [ -f /etc/systemd/system/tmp.mount.d/nebulactrl.conf ] && systemctl is-enabled --quiet tmp.mount 2>/dev/null; then echo "takes effect after the next reboot"; exit 0; fi
exit 1`,
			Apply: `nc_state
mkdir -p /etc/systemd/system/tmp.mount.d
printf '# Managed by NebulaCtrl host hardening. Delete this file to undo it.\n[Mount]\nOptions=mode=1777,strictatime,nosuid,nodev\n' > /etc/systemd/system/tmp.mount.d/nebulactrl.conf
systemctl daemon-reload
if ! systemctl enable tmp.mount >/dev/null 2>&1; then rm -f /etc/systemd/system/tmp.mount.d/nebulactrl.conf; systemctl daemon-reload; echo "systemctl enable tmp.mount failed"; exit 1; fi
nc_note "/tmp becomes a tmpfs at the next boot"
echo @@REBOOT`,
			Rollback: `rm -f /etc/systemd/system/tmp.mount.d/nebulactrl.conf
systemctl disable tmp.mount >/dev/null 2>&1 || true
systemctl daemon-reload`,
			Manual: []string{
				"sudo mkdir -p /etc/systemd/system/tmp.mount.d",
				"printf '[Mount]\\nOptions=mode=1777,strictatime,nosuid,nodev\\n' | sudo tee /etc/systemd/system/tmp.mount.d/nebulactrl.conf",
				"sudo systemctl enable tmp.mount   # mounted at the next boot",
			},
		},
		{
			ID:    "auditd",
			Title: "Install and enable auditd with a minimal rule set",
			Why:   "The Linux audit daemon records changes to account and sudo files with the user who made them, which is what you need after an incident. The rules watch /etc/passwd, group, shadow, gshadow and sudoers only.",
			Risk:  "Installs the auditd package and runs a daemon. Audit logs grow (8 MB files, 5 kept by default) and each watched write costs a few microseconds. Not available inside containers.",
			Tests: []string{"ACCT-9628", "ACCT-9630"}, Default: false,
			Precondition: `command -v systemctl >/dev/null 2>&1 || { echo "this host does not run systemd"; exit 1; }
if command -v systemd-detect-virt >/dev/null 2>&1 && systemd-detect-virt -cq; then echo "the audit subsystem is not available in a container"; exit 1; fi
command -v apt-get >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1 || command -v auditctl >/dev/null 2>&1 || { echo "no supported package manager to install auditd"; exit 1; }`,
			Check: `systemctl is-active --quiet auditd 2>/dev/null && [ -f /etc/audit/rules.d/nebulactrl.rules ]`,
			Apply: `nc_state
if ! command -v auditctl >/dev/null 2>&1; then
  : > "$S/auditd.installed-by-us"
  if command -v apt-get >/dev/null 2>&1; then DEBIAN_FRONTEND=noninteractive apt-get install -y -qq auditd >/dev/null
  else dnf install -y -q audit >/dev/null; fi
fi
systemctl is-active --quiet auditd 2>/dev/null || : > "$S/auditd.started-by-us"
mkdir -p /etc/audit/rules.d
printf '## Managed by NebulaCtrl host hardening. Delete this file to undo it.\n-w /etc/passwd -p wa -k identity\n-w /etc/group -p wa -k identity\n-w /etc/shadow -p wa -k identity\n-w /etc/gshadow -p wa -k identity\n-w /etc/sudoers -p wa -k privilege\n-w /etc/sudoers.d/ -p wa -k privilege\n' > /etc/audit/rules.d/nebulactrl.rules
chmod 640 /etc/audit/rules.d/nebulactrl.rules
systemctl enable auditd >/dev/null 2>&1 || true
systemctl start auditd >/dev/null 2>&1 || true
augenrules --load >/dev/null 2>&1 || true
nc_note "auditd running with identity and sudo watches"`,
			Rollback: `rm -f /etc/audit/rules.d/nebulactrl.rules
augenrules --load >/dev/null 2>&1 || true
if [ -f "$S/auditd.started-by-us" ]; then systemctl disable --now auditd >/dev/null 2>&1 || true; rm -f "$S/auditd.started-by-us"; fi
if [ -f "$S/auditd.installed-by-us" ]; then nc_note "the auditd package stays installed; remove it with your package manager if you want"; rm -f "$S/auditd.installed-by-us"; fi`,
			Manual: []string{
				"sudo apt-get install -y auditd   # dnf install audit on RHEL",
				"printf -- '-w /etc/passwd -p wa -k identity\\n-w /etc/shadow -p wa -k identity\\n-w /etc/sudoers -p wa -k privilege\\n' | sudo tee /etc/audit/rules.d/nebulactrl.rules",
				"sudo systemctl enable --now auditd && sudo augenrules --load",
			},
		},
	}
	return fixes
}

// ByID returns the fix with the given ID.
func ByID(id string) (Fix, bool) {
	for _, f := range Catalog() {
		if f.ID == id {
			return f, true
		}
	}
	return Fix{}, false
}

// Defaults returns the IDs of the fixes "Apply safe fixes" applies.
func Defaults() []string {
	var ids []string
	for _, f := range Catalog() {
		if f.Default {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// Select returns the fixes with the given IDs in catalog order, and the IDs
// that are not in the catalog.
func Select(ids []string) (fixes []Fix, unknown []string) {
	for _, f := range Catalog() {
		if slices.Contains(ids, f.ID) {
			fixes = append(fixes, f)
		}
	}
	for _, id := range ids {
		if _, ok := ByID(id); !ok {
			unknown = append(unknown, id)
		}
	}
	return fixes, unknown
}
