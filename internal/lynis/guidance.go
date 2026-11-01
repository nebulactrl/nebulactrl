package lynis

import "strings"

// Guidance explains a Lynis finding NebulaCtrl does not fix by itself, or only
// fixes on request: what it means, what to type to deal with it, and, when
// NebulaCtrl deliberately leaves it alone, why.
type Guidance struct {
	Explanation string
	// Manual commands for Debian and Ubuntu; RHEL-family hosts use dnf for apt.
	Manual []string
	// Never is set when NebulaCtrl will not change this, whoever asks.
	Never string
}

// ControlURL is CISOfy's page for a Lynis control.
func ControlURL(id string) string {
	base, _, _ := strings.Cut(id, ":")
	return "https://cisofy.com/lynis/controls/" + base + "/"
}

const (
	neverSSH      = "NebulaCtrl never changes SSH settings: a wrong value can lock you out of the host, and the installer promises not to. Make the change yourself and keep a second session open while you test it."
	neverFirewall = "NebulaCtrl never changes firewall rules beyond the ones the installer sets with ufw (SSH, 80, 443). Rules are specific to what runs on the host."
)

var testGuidance = map[string]Guidance{
	"SSH-7408": {
		Explanation: "Lynis compares sshd's effective options with its profile (for example MaxAuthTries, AllowTcpForwarding, X11Forwarding). Several of them, AllowTcpForwarding and AllowAgentForwarding for example, are used by ordinary admin workflows such as tunnels and jump hosts.",
		Manual: []string{
			"sudo sshd -T | sort   # the options sshd really uses",
			"sudoedit /etc/ssh/sshd_config.d/10-hardening.conf",
			"sudo sshd -t && sudo systemctl reload ssh   # test the file first; keep this session open and log in a second time before closing it",
		},
		Never: neverSSH,
	},
	"AUTH-9262": {
		Explanation: "No PAM module checks the strength of new passwords. It only matters for accounts that log in with a password; key-only hosts are unaffected.",
		Manual:      []string{"sudo apt-get install libpam-pwquality", "sudoedit /etc/security/pwquality.conf   # minlen, minclass"},
		Never:       "It edits the PAM login stack. A mistake there can lock every account out, so NebulaCtrl leaves it to you.",
	},
	"AUTH-9284": {
		Explanation: "Some accounts are locked (their password field starts with ! or *). That is normal for service accounts; Lynis only asks you to look at the list.",
		Manual:      []string{"sudo passwd -S -a | awk '$2 == \"L\" {print $1}'"},
	},
	"AUTH-9286": {
		Explanation: "Password ageing defaults are missing from /etc/login.defs. The 'Set password ageing defaults' fix sets them for new accounts. Existing accounts are never changed by NebulaCtrl.",
		Manual:      []string{"sudo chage -l <user>   # see an account's ageing", "sudo chage -M 365 -m 1 <user>   # change one account yourself"},
	},
	"BOOT-5180": {Explanation: "Informational: Lynis could not determine the boot runlevel and services."},
	"BOOT-5264": {
		Explanation: "systemd can sandbox services (read-only filesystem, private /tmp, no new privileges). Lynis lists each service with its exposure score.",
		Manual:      []string{"systemd-analyze security", "systemd-analyze security <service>"},
		Never:       "Sandboxing a service changes how it runs and can break it; it has to be tested per service.",
	},
	"KRNL-5788": {Explanation: "Lynis could not find the kernel image in /boot. That is expected inside a container or on a host that boots a kernel supplied by the hypervisor."},
	"FILE-6310": {
		Explanation: "A full /home or /var can take the host down; separate partitions contain that. Moving data onto new partitions needs downtime and a plan for your disk layout, so there is no safe automatic fix. /tmp is covered by the optional 'Mount /tmp as a separate tmpfs' fix.",
		Manual:      []string{"findmnt /home /var /tmp", "lsblk -f"},
		Never:       "It means repartitioning disks.",
	},
	"FILE-7524": {
		Explanation: "A file or directory is more permissive than Lynis's profile. NebulaCtrl tightens the cron files with a fix. The rest is left to you: /etc/ssh/sshd_config at 644 does no harm (sshd reads it as root, and it holds no secrets); the allow and deny lists of cron and at are read through setgid helpers on Debian and Ubuntu.",
		Manual:      []string{"sudo chmod 600 /etc/ssh/sshd_config   # safe: sshd reads it as root", "ls -l /etc/cron.allow /etc/cron.deny /etc/at.allow /etc/at.deny 2>/dev/null"},
	},
	"USB-1000": {
		Explanation: "USB storage can be mounted by anyone with physical access. The optional 'Block USB storage' fix prevents it.",
		Manual:      []string{"echo 'install usb-storage /bin/false' | sudo tee /etc/modprobe.d/nebulactrl-usb-storage.conf"},
	},
	"NAME-4028": {
		Explanation: "The host has no DNS domain name. Harmless for a standalone server.",
		Manual:      []string{"hostnamectl", "grep -E '^(search|domain)' /etc/resolv.conf"},
	},
	"PKGS-7370": {Explanation: "debsums verifies installed files against the packages' checksums, which helps find tampered binaries.", Manual: []string{"sudo apt-get install debsums", "sudo debsums -s"}},
	"PKGS-7392": {
		Explanation: "Packages with known vulnerabilities are installed. The installer turns on automatic security updates; run an update now.",
		Manual:      []string{"sudo apt-get update && sudo apt-get upgrade", "sudo dnf upgrade --security   # RHEL family"},
		Never:       "NebulaCtrl never upgrades packages for you: an upgrade can restart services.",
	},
	"PKGS-7394": {Explanation: "apt-show-versions lists packages that can be upgraded, which patch management tools use.", Manual: []string{"sudo apt-get install apt-show-versions"}},
	"PKGS-7420": {
		Explanation: "No tool applies upgrades automatically. The installer sets up unattended-upgrades (Debian, Ubuntu) or dnf-automatic (RHEL family) unless you passed --no-auto-updates.",
		Manual:      []string{"sudo apt-get install unattended-upgrades && sudo dpkg-reconfigure -plow unattended-upgrades"},
	},
	"LOGG-2154": {
		Explanation: "Logs stay on the host, so an intruder can erase them. Ship them off the machine: NebulaCtrl's log drains send service logs to Loki, OTLP, syslog or an HTTP endpoint; for the host's own logs use rsyslog or journald forwarding.",
		Manual:      []string{"sudo apt-get install rsyslog", "echo '*.* @@logs.example.com:514' | sudo tee /etc/rsyslog.d/90-remote.conf && sudo systemctl restart rsyslog"},
		Never:       "It needs the address of your log host.",
	},
	"ACCT-9622": {Explanation: "Process accounting records every command run on the host.", Manual: []string{"sudo apt-get install acct"}},
	"ACCT-9626": {Explanation: "sysstat collects CPU, memory and disk history. NebulaCtrl's own host metrics cover the basics.", Manual: []string{"sudo apt-get install sysstat", "sudo sed -i 's/^ENABLED=.*/ENABLED=\"true\"/' /etc/default/sysstat && sudo systemctl enable --now sysstat"}},
	"ACCT-9628": {
		Explanation: "auditd is not running. The optional 'Install and enable auditd' fix sets it up with a minimal rule set; it is unavailable inside containers.",
		Manual:      []string{"sudo apt-get install auditd", "sudo systemctl enable --now auditd"},
	},
	"ACCT-9630": {Explanation: "auditd has no rules. The optional auditd fix adds watches on account and sudo files."},
	"FINT-4350": {
		Explanation: "A file integrity tool alerts when system files change. AIDE is the usual open-source choice; it needs a baseline and a place for the reports.",
		Manual:      []string{"sudo apt-get install aide", "sudo aideinit && sudo aide --check"},
		Never:       "It needs a baseline you control and somewhere to send the alerts.",
	},
	"HRDN-7230": {
		Explanation: "No malware scanner is installed. rkhunter and chkrootkit look for rootkits; ClamAV scans files.",
		Manual:      []string{"sudo apt-get install rkhunter", "sudo rkhunter --update && sudo rkhunter --check --sk"},
		Never:       "A scanner needs tuning to your host or it floods you with false positives.",
	},
	"HRDN-7222": {
		Explanation: "Compilers are installed and usable by every user, which helps an intruder build exploits.",
		Manual:      []string{"sudo chmod o-x \"$(readlink -f /usr/bin/gcc)\" \"$(readlink -f /usr/bin/cc)\""},
		Never:       "It breaks local builds, DKMS kernel modules and some package installs.",
	},
	"TOOL-5002": {Explanation: "No configuration management tool (Ansible, Puppet, Salt ...) was found. Informational."},
	"NETW-3200": {
		Explanation: "An uncommon network protocol module is available. The 'Block rarely used network protocols' fix blocks DCCP, SCTP, RDS and TIPC. A module that is loaded right now is left alone.",
		Manual:      []string{"lsmod | grep -E 'dccp|sctp|rds|tipc'"},
	},
}

// keyGuidance covers sysctl keys that Lynis wants and NebulaCtrl does not set.
var keyGuidance = map[string]Guidance{
	"net.ipv4.conf.all.forwarding": {
		Explanation: "Lynis wants IP forwarding off. Docker routes container traffic through it and publishes ports with it: turning it off stops every container from reaching the network.",
		Never:       "Docker needs IPv4 forwarding on this host.",
	},
	"net.ipv4.ip_forward": {
		Explanation: "Docker routes container traffic through IP forwarding.",
		Never:       "Docker needs IPv4 forwarding on this host.",
	},
	"net.ipv4.conf.all.rp_filter": {
		Explanation: "Strict reverse-path filtering drops packets that would leave through another interface than they arrived on. That breaks hosts with more than one uplink, Tailscale and WireGuard policy routing, and some VPN setups. Loose mode (2) is the safe choice if you want it.",
		Manual:      []string{"echo 'net.ipv4.conf.all.rp_filter = 2' | sudo tee /etc/sysctl.d/90-rp-filter.conf && sudo sysctl --system"},
		Never:       "Strict filtering breaks asymmetric routes, Tailscale and WireGuard.",
	},
	"kernel.modules_disabled": {
		Explanation: "Setting it to 1 forbids loading any kernel module until the next reboot, with no way back. Docker loads modules (overlay, br_netfilter, iptables) and so do USB and network hot-plug.",
		Never:       "It is one-way until reboot and breaks Docker.",
	},
	"kernel.unprivileged_bpf_disabled": {
		Explanation: "Value 2 already blocks unprivileged BPF but lets an administrator change it back; 1 locks it until the next reboot. The difference is small and the lock cannot be undone at runtime.",
		Manual:      []string{"echo 'kernel.unprivileged_bpf_disabled = 1' | sudo tee /etc/sysctl.d/90-bpf.conf && sudo sysctl --system"},
		Never:       "It cannot be rolled back without a reboot.",
	},
	"fs.protected_fifos": {
		Explanation: "Covered by the optional 'Restrict FIFO and regular file creation in sticky directories' fix, which can make root jobs that open other users' lock files in /tmp fail.",
	},
	"fs.protected_regular": {
		Explanation: "Covered by the optional 'Restrict FIFO and regular file creation in sticky directories' fix.",
	},
	"kernel.perf_event_paranoid": {
		Explanation: "Restricts unprivileged use of perf. Raising it breaks profilers run by non-root users.",
		Manual:      []string{"echo 'kernel.perf_event_paranoid = 3' | sudo tee /etc/sysctl.d/90-perf.conf && sudo sysctl --system"},
	},
}

// GuidanceFor returns the guidance for a Lynis test ID. Findings of SSH and
// firewall tests always carry a Never; unknown tests get an empty Guidance.
func GuidanceFor(id string) Guidance {
	base, _, _ := strings.Cut(id, ":")
	if g, ok := testGuidance[base]; ok {
		return g
	}
	switch {
	case strings.HasPrefix(base, "SSH-"):
		return Guidance{Never: neverSSH}
	case strings.HasPrefix(base, "FIRE-"):
		return Guidance{Never: neverFirewall}
	}
	return Guidance{}
}

// KeyGuidanceFor returns the guidance for one sysctl key.
func KeyGuidanceFor(key string) (Guidance, bool) {
	g, ok := keyGuidance[key]
	return g, ok
}
