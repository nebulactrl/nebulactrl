package lynis

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestCatalogInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Catalog() {
		require.False(t, seen[f.ID], "duplicate fix %s", f.ID)
		seen[f.ID] = true
		require.NotEmpty(t, f.Title, f.ID)
		require.NotEmpty(t, f.Why, f.ID)
		require.NotEmpty(t, f.Risk, f.ID)
		require.NotEmpty(t, f.Tests, f.ID)
		require.NotEmpty(t, f.Check, f.ID)
		require.NotEmpty(t, f.Apply, f.ID)
		require.NotEmpty(t, f.Rollback, f.ID)
		require.NotEmpty(t, f.Manual, f.ID)
		for _, id := range f.Tests {
			require.True(t, validTestID(id), "%s: bad Lynis test ID %s", f.ID, id)
		}
		if len(f.Keys) > 0 {
			require.Contains(t, f.Tests, "KRNL-6000", f.ID)
		}
		if f.Reboot {
			require.False(t, f.Default, "%s: a fix that needs a reboot is never part of the default set", f.ID)
		}
	}
	require.Subset(t, Defaults(), []string{"sysctl-kernel", "sysctl-net", "cron-permissions", "umask"})
	require.NotContains(t, Defaults(), "auditd")
	require.NotContains(t, Defaults(), "tmp-mount")
	require.NotContains(t, Defaults(), "usb-storage")
	require.NotContains(t, Defaults(), "sysctl-fs-protected")
}

// TestCatalogNeverTouches pins the promises of the Fix comment: nothing about
// SSH, the firewall, accounts, or the sysctls Docker and Tailscale depend on.
func TestCatalogNeverTouches(t *testing.T) {
	forbidden := []string{
		"ssh", "sshd", "ufw", "iptables", "nft ", "firewall", "ip_forward", "forwarding", "modules_disabled", "rp_filter",
		"chage", "usermod", "useradd", "passwd -l", "passwd -e", "passwd -d", "chpasswd", "PermitRootLogin", "PasswordAuthentication", "authorized_keys", "docker",
	}
	for _, f := range Catalog() {
		text := strings.ToLower(f.Precondition + "\n" + f.Check + "\n" + f.Apply + "\n" + f.Rollback + "\n" + strings.Join(f.Manual, "\n") + "\n" + strings.Join(f.Keys, "\n"))
		for _, bad := range forbidden {
			require.NotContains(t, text, strings.ToLower(bad), "fix %s must not touch %q", f.ID, bad)
		}
	}
}

func TestEveryScriptIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, action := range []string{ScriptStatus, ScriptApply, ScriptRollback} {
		script := Script(action, Catalog())
		require.Less(t, len(script), 100_000, "the helper container passes the script as one argument")
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", action, out)
	}
}

func TestSelectAndByID(t *testing.T) {
	fixes, unknown := Select([]string{"umask", "core-dumps", "nope"})
	require.Equal(t, []string{"core-dumps", "umask"}, []string{fixes[0].ID, fixes[1].ID}, "catalog order")
	require.Equal(t, []string{"nope"}, unknown)
	_, ok := ByID("umask")
	require.True(t, ok)
	require.Equal(t, "https://cisofy.com/lynis/controls/KRNL-6000/", ControlURL("KRNL-6000"))
}

// ---- running the real scripts against a scratch root ----

type host struct {
	t    *testing.T
	root string
}

func newHost(t *testing.T) *host {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	h := &host{t: t, root: t.TempDir()}
	require.NoError(t, os.MkdirAll(filepath.Join(h.root, "tmp"), 0o755))
	return h
}

func (h *host) path(p string) string { return filepath.Join(h.root, p) }

func (h *host) write(p, content string) {
	h.t.Helper()
	require.NoError(h.t, os.MkdirAll(filepath.Dir(h.path(p)), 0o755))
	require.NoError(h.t, os.WriteFile(h.path(p), []byte(content), 0o644))
}

func (h *host) read(p string) string {
	h.t.Helper()
	b, err := os.ReadFile(h.path(p))
	require.NoError(h.t, err)
	return string(b)
}

func (h *host) exists(p string) bool { _, err := os.Stat(h.path(p)); return err == nil }

func (h *host) sysctl(key, val string) {
	h.write("proc/sys/"+strings.ReplaceAll(key, ".", "/"), val+"\n")
}

func (h *host) sysctlValue(key string) string {
	return strings.TrimSpace(h.read("proc/sys/" + strings.ReplaceAll(key, ".", "/")))
}

func (h *host) run(script string) string {
	h.t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"NC_ROOT=" + h.root, "TMPDIR=" + h.path("tmp"), "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	require.NoError(h.t, err, string(out))
	return string(out)
}

func (h *host) status(fixes ...Fix) map[string]agentprotocol.HardeningFixState {
	st, _, _ := ParseDriverOutput(h.run(Script(ScriptStatus, fixes)))
	m := map[string]agentprotocol.HardeningFixState{}
	for _, s := range st {
		m[s.ID] = s
	}
	return m
}

func (h *host) do(action string, fixes ...Fix) (map[string]agentprotocol.HardeningFixOutcome, bool) {
	_, oc, reboot := ParseDriverOutput(h.run(Script(action, fixes)))
	m := map[string]agentprotocol.HardeningFixOutcome{}
	for _, o := range oc {
		m[o.ID] = o
	}
	return m, reboot
}

func fix(t *testing.T, id string) Fix {
	t.Helper()
	f, ok := ByID(id)
	require.True(t, ok, id)
	return f
}

func TestSysctlApplyVerifyRollback(t *testing.T) {
	h := newHost(t)
	f := fix(t, "sysctl-kernel")
	h.sysctl("kernel.dmesg_restrict", "0")
	h.sysctl("kernel.kptr_restrict", "0")
	h.sysctl("kernel.sysrq", "438")
	h.sysctl("kernel.ctrl-alt-del", "1")
	h.sysctl("kernel.yama.ptrace_scope", "3") // already stricter than ours: stays
	h.sysctl("dev.tty.ldisc_autoload", "1")
	// net.core.bpf_jit_harden and kernel.core_uses_pid do not exist on this "kernel".

	require.Equal(t, agentprotocol.HardeningFixPending, h.status(f)["sysctl-kernel"].State)

	out, _ := h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["sysctl-kernel"].Outcome, out["sysctl-kernel"].Message)
	require.Equal(t, "1", h.sysctlValue("kernel.dmesg_restrict"))
	require.Equal(t, "2", h.sysctlValue("kernel.kptr_restrict"))
	require.Equal(t, "0", h.sysctlValue("kernel.sysrq"))
	require.Equal(t, "0", h.sysctlValue("kernel.ctrl-alt-del"))
	require.Equal(t, "3", h.sysctlValue("kernel.yama.ptrace_scope"), "a stricter value is never loosened")
	require.Equal(t, "0", h.sysctlValue("dev.tty.ldisc_autoload"))
	require.False(t, h.exists("proc/sys/net/core/bpf_jit_harden"), "absent keys are not invented")

	conf := h.read("etc/sysctl.d/90-nebulactrl-sysctl-kernel.conf")
	require.Contains(t, conf, "kernel.kptr_restrict = 2")
	require.Contains(t, conf, "Managed by NebulaCtrl")
	require.NotContains(t, conf, "ptrace_scope", "only what changed is persisted")
	require.Equal(t, agentprotocol.HardeningFixApplied, h.status(f)["sysctl-kernel"].State)

	// Applying again changes nothing.
	out, _ = h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeUnchanged, out["sysctl-kernel"].Outcome)

	out, _ = h.do(ScriptRollback, f)
	require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, out["sysctl-kernel"].Outcome, out["sysctl-kernel"].Message)
	require.Equal(t, "0", h.sysctlValue("kernel.dmesg_restrict"))
	require.Equal(t, "0", h.sysctlValue("kernel.kptr_restrict"))
	require.Equal(t, "438", h.sysctlValue("kernel.sysrq"))
	require.Equal(t, "1", h.sysctlValue("kernel.ctrl-alt-del"))
	require.Equal(t, "3", h.sysctlValue("kernel.yama.ptrace_scope"))
	require.False(t, h.exists("etc/sysctl.d/90-nebulactrl-sysctl-kernel.conf"))
	require.Equal(t, agentprotocol.HardeningFixPending, h.status(f)["sysctl-kernel"].State)
}

func TestRollbackOnlyUndoesWhatNebulaCtrlApplied(t *testing.T) {
	h := newHost(t)
	f := fix(t, "sysctl-fs")
	h.sysctl("fs.suid_dumpable", "0")
	h.sysctl("fs.protected_hardlinks", "1")
	h.sysctl("fs.protected_symlinks", "1")
	require.Equal(t, agentprotocol.HardeningFixCompliant, h.status(f)["sysctl-fs"].State, "already fine: not ours")
	out, _ := h.do(ScriptRollback, f)
	require.Equal(t, agentprotocol.HardeningOutcomeSkipped, out["sysctl-fs"].Outcome)
	require.Contains(t, out["sysctl-fs"].Message, "did not apply")
	out, _ = h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeUnchanged, out["sysctl-fs"].Outcome)
}

func TestNetworkFixNeverTouchesForwarding(t *testing.T) {
	h := newHost(t)
	f := fix(t, "sysctl-net")
	h.sysctl("net.ipv4.ip_forward", "1")
	h.sysctl("net.ipv4.conf.all.forwarding", "1")
	h.sysctl("net.ipv4.conf.all.rp_filter", "2")
	h.sysctl("net.ipv4.conf.all.accept_redirects", "1")
	h.sysctl("net.ipv4.conf.all.log_martians", "0")
	out, _ := h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["sysctl-net"].Outcome, out["sysctl-net"].Message)
	require.Equal(t, "0", h.sysctlValue("net.ipv4.conf.all.accept_redirects"))
	require.Equal(t, "1", h.sysctlValue("net.ipv4.conf.all.log_martians"))
	require.Equal(t, "1", h.sysctlValue("net.ipv4.ip_forward"))
	require.Equal(t, "1", h.sysctlValue("net.ipv4.conf.all.forwarding"))
	require.Equal(t, "2", h.sysctlValue("net.ipv4.conf.all.rp_filter"))
}

const loginDefsOriginal = "# login.defs\nMAIL_DIR\t/var/mail\nUMASK\t\t022\nPASS_MAX_DAYS\t99999\nPASS_MIN_DAYS\t0\nPASS_WARN_AGE\t7\n#SHA_CRYPT_MIN_ROUNDS 5000\nENCRYPT_METHOD YESCRYPT\n"

func TestLoginDefsFixesApplyAndRestoreExactly(t *testing.T) {
	h := newHost(t)
	h.write("etc/login.defs", loginDefsOriginal)
	umask, ageing := fix(t, "umask"), fix(t, "password-ageing")

	out, _ := h.do(ScriptApply, umask, ageing)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["umask"].Outcome, out["umask"].Message)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["password-ageing"].Outcome, out["password-ageing"].Message)
	got := h.read("etc/login.defs")
	require.Regexp(t, `(?m)^UMASK\s+027$`, got)
	require.Regexp(t, `(?m)^PASS_MAX_DAYS\s+365$`, got)
	require.Regexp(t, `(?m)^PASS_MIN_DAYS\s+1$`, got)
	require.Regexp(t, `(?m)^SHA_CRYPT_MIN_ROUNDS\s+5000$`, got)
	require.Regexp(t, `(?m)^SHA_CRYPT_MAX_ROUNDS\s+10000$`, got)
	require.Contains(t, got, "#SHA_CRYPT_MIN_ROUNDS 5000", "comments are kept")
	require.Contains(t, got, "MAIL_DIR\t/var/mail")
	require.Contains(t, got, "PASS_WARN_AGE\t7")

	// An edit made after our change survives the rollback.
	h.write("etc/login.defs", strings.Replace(got, "PASS_WARN_AGE\t7", "PASS_WARN_AGE\t14", 1))
	out, _ = h.do(ScriptRollback, umask, ageing)
	require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, out["umask"].Outcome, out["umask"].Message)
	require.Equal(t, strings.Replace(loginDefsOriginal, "PASS_WARN_AGE\t7", "PASS_WARN_AGE\t14", 1), h.read("etc/login.defs"))
}

func TestLoginDefsKeepsStricterValues(t *testing.T) {
	h := newHost(t)
	h.write("etc/login.defs", "UMASK 077\nPASS_MAX_DAYS 90\nPASS_MIN_DAYS 7\nSHA_CRYPT_MIN_ROUNDS 20000\nSHA_CRYPT_MAX_ROUNDS 30000\n")
	st := h.status(fix(t, "umask"), fix(t, "password-ageing"))
	require.Equal(t, agentprotocol.HardeningFixCompliant, st["umask"].State)
	require.Equal(t, agentprotocol.HardeningFixCompliant, st["password-ageing"].State)
	out, _ := h.do(ScriptApply, fix(t, "umask"), fix(t, "password-ageing"))
	require.Equal(t, agentprotocol.HardeningOutcomeUnchanged, out["umask"].Outcome)
	require.Contains(t, h.read("etc/login.defs"), "PASS_MAX_DAYS 90")
}

func TestLoginDefsFixUnavailableWithoutFile(t *testing.T) {
	h := newHost(t)
	st := h.status(fix(t, "umask"))
	require.Equal(t, agentprotocol.HardeningFixUnavailable, st["umask"].State)
	require.Contains(t, st["umask"].Message, "login.defs")
	out, _ := h.do(ScriptApply, fix(t, "umask"))
	require.Equal(t, agentprotocol.HardeningOutcomeSkipped, out["umask"].Outcome)
}

func TestCronPermissionsTightenOnly(t *testing.T) {
	h := newHost(t)
	h.write("etc/crontab", "# crontab\n")
	require.NoError(t, os.Chmod(h.path("etc/crontab"), 0o644))
	require.NoError(t, os.MkdirAll(h.path("etc/cron.daily"), 0o755))
	require.NoError(t, os.Chmod(h.path("etc/cron.daily"), 0o755))
	require.NoError(t, os.MkdirAll(h.path("etc/cron.d"), 0o700)) // already fine
	// cron.hourly is absent.
	f := fix(t, "cron-permissions")
	require.Equal(t, agentprotocol.HardeningFixPending, h.status(f)["cron-permissions"].State)
	out, _ := h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["cron-permissions"].Outcome, out["cron-permissions"].Message)
	mode := func(p string) os.FileMode {
		st, err := os.Stat(h.path(p))
		require.NoError(t, err)
		return st.Mode().Perm()
	}
	require.Equal(t, os.FileMode(0o600), mode("etc/crontab"))
	require.Equal(t, os.FileMode(0o700), mode("etc/cron.daily"))
	require.False(t, h.exists("etc/cron.hourly"))
	out, _ = h.do(ScriptRollback, f)
	require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, out["cron-permissions"].Outcome, out["cron-permissions"].Message)
	require.Equal(t, os.FileMode(0o644), mode("etc/crontab"))
	require.Equal(t, os.FileMode(0o755), mode("etc/cron.daily"))
	require.Equal(t, os.FileMode(0o700), mode("etc/cron.d"), "a directory that was already 700 is not recorded, so it is not 'restored'")
}

func TestModprobeBlocksOnlyUnusedModules(t *testing.T) {
	h := newHost(t)
	h.write("proc/modules", "sctp 5 0 - Live 0x0\nnf_tables 3 1 - Live 0x1\n")
	f := fix(t, "protocols")
	require.Equal(t, agentprotocol.HardeningFixPending, h.status(f)["protocols"].State)
	out, _ := h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["protocols"].Outcome, out["protocols"].Message)
	conf := h.read("etc/modprobe.d/nebulactrl-protocols.conf")
	for _, m := range []string{"dccp", "rds", "tipc"} {
		// The exact lines Lynis NETW-3200 greps for.
		require.Regexp(t, `(?m)^install `+m+` /bin/false$`, conf)
		require.Regexp(t, `(?m)^blacklist `+m+`$`, conf)
	}
	require.NotContains(t, conf, "sctp", "a module that is loaded right now is left alone")
	require.Equal(t, agentprotocol.HardeningFixApplied, h.status(f)["protocols"].State, "a loaded module counts as compliant")
	out, _ = h.do(ScriptRollback, f)
	require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, out["protocols"].Outcome)
	require.False(t, h.exists("etc/modprobe.d/nebulactrl-protocols.conf"))
}

func TestBannerKeepsOriginalAndRestoresIt(t *testing.T) {
	h := newHost(t)
	h.write("etc/issue", "Debian GNU/Linux 13 \\n \\l\n\n")
	h.write("etc/issue.net", "Debian GNU/Linux 13\n")
	f := fix(t, "legal-banner")
	require.Equal(t, agentprotocol.HardeningFixPending, h.status(f)["legal-banner"].State)
	out, _ := h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["legal-banner"].Outcome, out["legal-banner"].Message)
	issue := h.read("etc/issue")
	require.True(t, strings.HasPrefix(issue, "Authorized access only."), issue)
	require.Contains(t, issue, "Debian GNU/Linux 13 \\n \\l", "the original text stays below the banner")
	out, _ = h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeUnchanged, out["legal-banner"].Outcome)

	out, _ = h.do(ScriptRollback, f)
	require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, out["legal-banner"].Outcome, out["legal-banner"].Message)
	require.Equal(t, "Debian GNU/Linux 13 \\n \\l\n\n", h.read("etc/issue"))
	require.Equal(t, "Debian GNU/Linux 13\n", h.read("etc/issue.net"))
}

func TestBannerRollbackKeepsLaterEdits(t *testing.T) {
	h := newHost(t)
	h.write("etc/issue", "orig\n")
	h.write("etc/issue.net", "orig\n")
	f := fix(t, "legal-banner")
	h.do(ScriptApply, f)
	h.write("etc/issue", "my own banner\n")
	out, _ := h.do(ScriptRollback, f)
	require.Contains(t, out["legal-banner"].Message, "edited since")
	require.Equal(t, "my own banner\n", h.read("etc/issue"))
	require.Equal(t, "orig\n", h.read("etc/issue.net"))
}

func TestCoreDumpsFix(t *testing.T) {
	h := newHost(t)
	f := fix(t, "core-dumps")
	out, _ := h.do(ScriptApply, f)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["core-dumps"].Outcome, out["core-dumps"].Message)
	require.Contains(t, h.read("etc/systemd/coredump.conf.d/nebulactrl-hardening.conf"), "Storage=none")
	require.Contains(t, h.read("etc/systemd/coredump.conf.d/nebulactrl-hardening.conf"), "ProcessSizeMax=0")
	require.Contains(t, h.read("etc/security/limits.d/nebulactrl-hardening.conf"), "* hard core 0")
	out, _ = h.do(ScriptRollback, f)
	require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, out["core-dumps"].Outcome)
	require.False(t, h.exists("etc/security/limits.d/nebulactrl-hardening.conf"))
}

func TestDriverRollsBackAFailingFixAndRejectsAnIneffectiveOne(t *testing.T) {
	h := newHost(t)
	failing := Fix{
		ID: "boom", Check: `[ -f "$R/etc/done" ]`,
		Apply:    `mkdir -p "$R/etc"; printf x > "$R/etc/half"; echo "disk is on fire"; exit 3`,
		Rollback: `rm -f "$R/etc/half"`,
	}
	noop := Fix{ID: "noop", Check: `exit 1`, Apply: `:`, Rollback: `:`}
	blocked := Fix{ID: "blocked", Precondition: `echo "this host is a container"; exit 1`, Check: `exit 1`, Apply: `touch "$R/etc/never"`, Rollback: `:`}
	reboot := Fix{ID: "later", Check: `[ -f "$R/etc/later" ]`, Apply: `touch "$R/etc/later"; echo @@REBOOT`, Rollback: `rm -f "$R/etc/later"`}

	out, rb := h.do(ScriptApply, failing, noop, blocked, reboot)
	require.Equal(t, agentprotocol.HardeningOutcomeFailed, out["boom"].Outcome)
	require.Contains(t, out["boom"].Message, "disk is on fire")
	require.False(t, h.exists("etc/half"), "the failed fix was rolled back")
	require.Equal(t, agentprotocol.HardeningOutcomeFailed, out["noop"].Outcome)
	require.Contains(t, out["noop"].Message, "did not take effect")
	require.Equal(t, agentprotocol.HardeningOutcomeSkipped, out["blocked"].Outcome)
	require.Contains(t, out["blocked"].Message, "container")
	require.False(t, h.exists("etc/never"))
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, out["later"].Outcome)
	require.True(t, rb, "a fix that needs a reboot is reported")

	st := h.status(failing, blocked, reboot)
	require.Equal(t, agentprotocol.HardeningFixPending, st["boom"].State)
	require.Equal(t, agentprotocol.HardeningFixUnavailable, st["blocked"].State)
	require.Contains(t, st["blocked"].Message, "container")
	require.Equal(t, agentprotocol.HardeningFixApplied, st["later"].State)
}

func TestRemainingMatchesFindingsToFixes(t *testing.T) {
	f, err := os.Open("testdata/report-debian13-before.dat")
	require.NoError(t, err)
	defer f.Close()
	rep, err := ParseReport(f)
	require.NoError(t, err)
	states := []agentprotocol.HardeningFixState{
		{ID: "sysctl-kernel", State: agentprotocol.HardeningFixPending},
		{ID: "protocols", State: agentprotocol.HardeningFixPending},
	}
	items := Remaining(rep, states)

	find := func(pred func(Item) bool) Item {
		t.Helper()
		for _, it := range items {
			if pred(it) {
				return it
			}
		}
		t.Fatal("item not found")
		return Item{}
	}
	kptr := find(func(i Item) bool { return strings.HasPrefix(i.Title, "kernel.kptr_restrict") })
	require.Equal(t, "sysctl-kernel", kptr.FixID)
	require.Equal(t, agentprotocol.HardeningFixPending, kptr.FixState)

	fwd := find(func(i Item) bool { return strings.HasPrefix(i.Title, "net.ipv4.conf.all.forwarding") })
	require.Empty(t, fwd.FixID)
	require.NotEmpty(t, fwd.Never, "Docker needs forwarding")
	rp := find(func(i Item) bool { return strings.HasPrefix(i.Title, "net.ipv4.conf.all.rp_filter") })
	require.NotEmpty(t, rp.Never)

	// Lynis lists SSH-7408 once per option; the options become one item each and the
	// bare repeats are dropped.
	var sshItems int
	for _, it := range items {
		if it.TestID == "SSH-7408" {
			sshItems++
			require.True(t, strings.HasPrefix(it.Title, "sshd option "), it.Title)
		}
	}
	require.Equal(t, 9, sshItems)
	ssh := find(func(i Item) bool { return i.TestID == "SSH-7408" })
	require.Contains(t, ssh.Never, "never changes SSH")
	require.Empty(t, ssh.FixID)
	require.True(t, strings.HasPrefix(ssh.Title, "sshd option "))

	proto := find(func(i Item) bool { return i.TestID == "NETW-3200" })
	require.Equal(t, "protocols", proto.FixID)
	require.Equal(t, "https://cisofy.com/lynis/controls/NETW-3200/", proto.Link)

	pkgs := find(func(i Item) bool { return i.TestID == "PKGS-7392" && i.Severity == SeverityWarning })
	require.NotEmpty(t, pkgs.Manual)

	// An unknown test still gets a link and Lynis's own text.
	unknown := Remaining(&agentprotocol.HardeningReport{Suggestions: []agentprotocol.HardeningFinding{{ID: "ZZZZ-1234", Text: "Do the thing", Solution: "Run foo"}}}, nil)
	require.Len(t, unknown, 1)
	require.Equal(t, "Run foo", unknown[0].Explanation)
	require.Empty(t, unknown[0].Never)
}

// TestDocsListEveryFix keeps docs/security/host-hardening.md in step with the
// catalog: each fix and each Lynis test ID it answers is in the table.
func TestDocsListEveryFix(t *testing.T) {
	raw, err := os.ReadFile("../../docs/security/host-hardening.md")
	require.NoError(t, err)
	doc := string(raw)
	for _, f := range Catalog() {
		require.Contains(t, doc, "`"+f.ID+"`", "fix %s is missing from the docs", f.ID)
		for _, id := range f.Tests {
			require.Contains(t, doc, id, "fix %s: Lynis test %s is missing from the docs", f.ID, id)
		}
	}
}
