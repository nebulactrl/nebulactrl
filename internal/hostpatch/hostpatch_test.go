package hostpatch

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const aptSimulation = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Inst libssl3 [3.0.2-0ubuntu1.10] (3.0.2-0ubuntu1.12 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
Inst openssl [3.0.2-0ubuntu1.10] (3.0.2-0ubuntu1.12 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
Inst docker-ce [5:26.1.3-1~ubuntu.22.04~jammy] (5:27.0.1-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
Inst linux-image-5.15.0-113-generic (5.15.0-113.123 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
Inst vim-tiny [2:8.2.3995-1ubuntu2.16] (2:8.2.3995-1ubuntu2.17 Ubuntu:22.04/jammy-updates [amd64])
Conf libssl3 (3.0.2-0ubuntu1.12 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
`

func TestParseAptSimulation(t *testing.T) {
	pkgs := ParseAptSimulation(aptSimulation)
	require.Equal(t, []agentprotocol.PatchPackage{
		{Name: "libssl3", From: "3.0.2-0ubuntu1.10", To: "3.0.2-0ubuntu1.12", Security: true},
		{Name: "openssl", From: "3.0.2-0ubuntu1.10", To: "3.0.2-0ubuntu1.12", Security: true},
		{Name: "docker-ce", From: "5:26.1.3-1~ubuntu.22.04~jammy", To: "5:27.0.1-1~ubuntu.22.04~jammy"},
		{Name: "linux-image-5.15.0-113-generic", To: "5.15.0-113.123", Security: true},
		{Name: "vim-tiny", From: "2:8.2.3995-1ubuntu2.16", To: "2:8.2.3995-1ubuntu2.17"},
	}, pkgs)
}

func TestParseAptDebianSecuritySuite(t *testing.T) {
	pkgs := ParseAptSimulation("Inst libc6 [2.36-9+deb12u3] (2.36-9+deb12u4 Debian-Security:12/stable-security [amd64])\n")
	require.Len(t, pkgs, 1)
	require.True(t, pkgs[0].Security)
}

const dnfAll = `
openssl.x86_64                     1:3.0.7-27.el9              baseos
kernel-core.x86_64                 5.14.0-427.28.1.el9_4       baseos
tzdata.noarch                      2024a-1.el9                 appstream
vim-minimal.x86_64                 2:8.2.2637-20.el9_1         baseos

Obsoleting Packages
foo.x86_64                         1.0-1.el9                   baseos
    old-foo.x86_64                 0.9-1.el9                   @baseos
`

const dnfSecurity = `
openssl.x86_64                     1:3.0.7-27.el9              baseos
kernel-core.x86_64                 5.14.0-427.28.1.el9_4       baseos
`

func TestParseDnfCheckUpdate(t *testing.T) {
	pkgs := ParseDnfCheckUpdate(dnfAll, dnfSecurity)
	require.Equal(t, []agentprotocol.PatchPackage{
		{Name: "openssl", To: "1:3.0.7-27.el9", Security: true},
		{Name: "kernel-core", To: "5.14.0-427.28.1.el9_4", Security: true},
		{Name: "tzdata", To: "2024a-1.el9"},
		{Name: "vim-minimal", To: "2:8.2.2637-20.el9_1"},
	}, pkgs, "the obsoleting section and wrapped lines are ignored")
}

func TestParseDnfNoUpdates(t *testing.T) {
	require.Empty(t, ParseDnfCheckUpdate("", ""))
}

func TestParseCheckApt(t *testing.T) {
	out := "@@BOOTID abc-123\n@@MANAGER apt\n@@REFRESH 0\n@@UPDATES\n" + aptSimulation + "@@UPDATESRC 0\n@@REBOOT 1\n@@REBOOTPKGS\nlinux-image-5.15.0-113-generic\nlinux-base\nlinux-base\n@@END\n"
	st, err := ParseCheck(out)
	require.NoError(t, err)
	require.Equal(t, "apt", st.Manager)
	require.True(t, st.Supported)
	require.Equal(t, 5, st.Updates)
	require.Equal(t, 3, st.Security)
	require.Equal(t, "abc-123", st.BootID)
	require.True(t, st.RebootRequired)
	require.Equal(t, []string{"linux-base", "linux-image-5.15.0-113-generic"}, st.RebootPackages, "deduplicated and sorted")
	require.Empty(t, st.Message)
	require.True(t, st.Packages[0].Security, "security updates are listed first")
	require.False(t, st.Packages[len(st.Packages)-1].Security)
}

func TestParseCheckAptRefreshFailed(t *testing.T) {
	st, err := ParseCheck("@@BOOTID b\n@@MANAGER apt\n@@REFRESH 100\n@@UPDATES\n@@UPDATESRC 0\n@@REBOOT 0\n@@END\n")
	require.NoError(t, err)
	require.Zero(t, st.Updates)
	require.False(t, st.RebootRequired)
	require.Contains(t, st.Message, "could not be refreshed")
}

func TestParseCheckDnf(t *testing.T) {
	out := "@@BOOTID b\n@@MANAGER dnf\n@@UPDATES\n" + dnfAll + "@@UPDATESRC 100\n@@SECURITY\n" + dnfSecurity + "@@SECURITYRC 100\n@@REBOOT 0\n@@END\n"
	st, err := ParseCheck(out)
	require.NoError(t, err)
	require.Equal(t, "dnf", st.Manager)
	require.Equal(t, 4, st.Updates)
	require.Equal(t, 2, st.Security)
	require.False(t, st.RebootRequired)
}

func TestParseCheckDnfError(t *testing.T) {
	_, err := ParseCheck("@@BOOTID b\n@@MANAGER dnf\n@@UPDATES\n@@UPDATESRC 1\n@@SECURITY\n@@SECURITYRC 1\n@@REBOOT 0\n@@END\n")
	require.ErrorContains(t, err, "check-update failed")
}

func TestParseCheckNoManager(t *testing.T) {
	st, err := ParseCheck("@@BOOTID b\n@@MANAGER none\n@@END\n")
	require.NoError(t, err)
	require.False(t, st.Supported)
	require.Empty(t, st.Manager)
	require.Contains(t, st.Message, "No supported package manager")
}

func TestParseCheckCutShort(t *testing.T) {
	_, err := ParseCheck("@@BOOTID b\n@@MANAGER apt\n@@UPDATES\n")
	require.ErrorContains(t, err, "cut short", "a truncated answer must not read as 'no updates'")
}

func TestParseCheckCapsPackages(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@BOOTID b\n@@MANAGER apt\n@@REFRESH 0\n@@UPDATES\n")
	for i := range 250 {
		b.WriteString("Inst pkg" + strings.Repeat("a", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + " [1] (2 Ubuntu:22.04/jammy-updates [amd64])\n")
	}
	b.WriteString("@@UPDATESRC 0\n@@REBOOT 0\n@@END\n")
	st, err := ParseCheck(b.String())
	require.NoError(t, err)
	require.Equal(t, 250, st.Updates, "the count is complete")
	require.Len(t, st.Packages, agentprotocol.MaxPatchPackages, "the list is capped")
}

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus("@@RC 0\n@@LOG\nReading package lists...\nSetting up openssl\n")
	require.NoError(t, err)
	require.Equal(t, UpgradeFinished, st.State)
	require.Zero(t, st.ExitCode)
	require.Equal(t, "Reading package lists...\nSetting up openssl", st.Log)

	st, err = ParseStatus("@@RC 100\n@@LOG\nE: dpkg was interrupted\n")
	require.NoError(t, err)
	require.Equal(t, 100, st.ExitCode)

	st, err = ParseStatus("@@RUNNING\n@@LOG\nUnpacking\n")
	require.NoError(t, err)
	require.Equal(t, UpgradeRunning, st.State)

	st, err = ParseStatus("@@LOST\n@@LOG\npartial\n")
	require.NoError(t, err)
	require.Equal(t, UpgradeLost, st.State)

	st, err = ParseStatus("@@NONE\n")
	require.NoError(t, err)
	require.Equal(t, UpgradeNone, st.State)

	_, err = ParseStatus("garbage")
	require.Error(t, err)
}

func TestParseStatusLogCannotForgeVerdict(t *testing.T) {
	st, err := ParseStatus("@@RUNNING\n@@LOG\n@@RC 0\n@@RC 0\n")
	require.NoError(t, err)
	require.Equal(t, UpgradeRunning, st.State, "marker-looking lines inside the log are log text")
	require.Contains(t, st.Log, "@@RC 0")
}

func TestParseStartAndReboot(t *testing.T) {
	for out, want := range map[string]StartState{"@@STARTED\n": StartStarted, "@@RUNNING\n": StartRunning, "@@DONE\n": StartDone} {
		got, err := ParseStart(out)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := ParseStart("@@FAILED cannot create /var/lib/x\n")
	require.ErrorContains(t, err, "cannot create")
	_, err = ParseStart("")
	require.Error(t, err)

	boot, how, err := ParseReboot("@@BOOTID b1\n@@SCHEDULED systemd\n")
	require.NoError(t, err)
	require.Equal(t, "b1", boot)
	require.Equal(t, "systemd", how)
	_, _, err = ParseReboot("@@BOOTID b1\n@@FAILED no way\n")
	require.ErrorContains(t, err, "no way")
	_, _, err = ParseReboot("@@BOOTID b1\n")
	require.Error(t, err)
}

func TestRisks(t *testing.T) {
	pkgs := []agentprotocol.PatchPackage{
		{Name: "vim"}, {Name: "docker-ce"}, {Name: "linux-image-6.1.0-21-amd64"}, {Name: "openssh-server"}, {Name: "containerd.io"},
	}
	risks := Risks(pkgs)
	names := make([]string, 0, len(risks))
	for _, r := range risks {
		names = append(names, r.Package)
	}
	require.Equal(t, []string{"docker-ce", "linux-image-6.1.0-21-amd64", "openssh-server", "containerd.io"}, names)
	require.True(t, RebootLikely(pkgs))
	require.True(t, RestartsDocker(pkgs))
	require.False(t, RebootLikely([]agentprotocol.PatchPackage{{Name: "vim"}, {Name: "docker-ce"}}))
	require.False(t, RestartsDocker([]agentprotocol.PatchPackage{{Name: "vim"}}))
	require.Empty(t, Risks(nil))
}

func TestScriptsAreValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	start := func(mgr string, sec bool) string {
		s, err := StartScript("0b1c2d3e-aaaa-bbbb-cccc-0123456789ab", mgr, sec)
		require.NoError(t, err)
		return s
	}
	status, err := StatusScript("0b1c2d3e-aaaa-bbbb-cccc-0123456789ab")
	require.NoError(t, err)
	for name, script := range map[string]string{
		"check": CheckScript, "reboot": RebootScript, "status": status,
		"start apt": start("apt", false), "start apt security": start("apt", true),
		"start dnf": start("dnf", false), "start dnf security": start("dnf", true),
	} {
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
	}
}

func TestScriptsRejectBadInput(t *testing.T) {
	for _, id := range []string{"", "x; rm -rf /", "../../etc", "a b"} {
		_, err := StartScript(id, "apt", false)
		require.Error(t, err, id)
		_, err = StatusScript(id)
		require.Error(t, err, id)
	}
	_, err := StartScript("0b1c2d3e-aaaa", "pacman", false)
	require.Error(t, err)
}

func TestSecurityOnlyUpgradeUsesSecurityFilter(t *testing.T) {
	s, err := StartScript("0b1c2d3e-aaaa", "apt", true)
	require.NoError(t, err)
	require.Contains(t, s, "--only-upgrade")
	require.Contains(t, s, "-security")
	s, err = StartScript("0b1c2d3e-aaaa", "dnf", true)
	require.NoError(t, err)
	require.Contains(t, s, "dnf -y upgrade --security")
	s, err = StartScript("0b1c2d3e-aaaa", "dnf", false)
	require.NoError(t, err)
	require.NotContains(t, s, "--security")
}

// The check script runs for real against the stub package managers below, so
// the section markers and the parser agree.
func TestCheckScriptAgainstStubs(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, exec.Command("sh", "-c", "printf '%s' \"$1\" > \"$2\" && chmod +x \"$2\"", "sh", body, dir+"/"+name).Run())
	}
	write("apt-get", `#!/bin/sh
case "$*" in
  *update*) exit 0 ;;
  *) printf 'Inst libssl3 [1] (2 Ubuntu:22.04/jammy-security [amd64])\nInst vim [1] (2 Ubuntu:22.04/jammy-updates [amd64])\n' ;;
esac
`)
	cmd := exec.Command(sh, "-c", CheckScript)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	out, err := cmd.Output()
	require.NoError(t, err)
	st, err := ParseCheck(string(out))
	require.NoError(t, err)
	require.Equal(t, "apt", st.Manager)
	require.Equal(t, 2, st.Updates)
	require.Equal(t, 1, st.Security)
}
