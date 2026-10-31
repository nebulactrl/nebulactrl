package hostpatch

import (
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Risk is a pending package whose upgrade deserves a warning, and why.
type Risk struct {
	Package string `json:"package"`
	Reason  string `json:"reason"`
	// Reboot is true when the upgrade only takes full effect after a reboot.
	Reboot bool `json:"reboot"`
}

type riskRule struct {
	exact    []string
	prefixes []string
	reason   string
	reboot   bool
}

var riskRules = []riskRule{
	{
		exact:    []string{"docker-ce", "docker-ce-cli", "docker.io", "docker-ce-rootless-extras", "containerd", "containerd.io", "runc", "moby-engine", "moby-containerd", "moby-runc"},
		prefixes: []string{"docker-ce-", "moby-"},
		reason:   "Restarts the Docker daemon. Unless live-restore is on, every container on the server stops until it is back.",
	},
	{
		exact:    []string{"kernel", "kernel-core", "linux-image-generic", "linux-generic", "linux-aws", "linux-azure", "linux-gcp", "linux-virtual"},
		prefixes: []string{"linux-image-", "linux-modules-", "linux-headers-", "kernel-core", "kernel-modules"},
		reason:   "A new kernel only runs after a reboot.", reboot: true,
	},
	{
		exact:  []string{"libc6", "libc-bin", "glibc", "glibc-common", "glibc-minimal-langpack"},
		reason: "Core C library: running programs keep the old one until they restart, so a reboot is advised.", reboot: true,
	},
	{
		exact: []string{"systemd", "systemd-libs", "systemd-sysv", "libsystemd0", "udev", "dbus"}, prefixes: []string{"systemd-"},
		reason: "Restarts system services; a reboot is advised.", reboot: true,
	},
	{
		prefixes: []string{"nvidia-", "libnvidia-", "kmod-nvidia", "xserver-xorg-video-nvidia"},
		reason:   "A GPU driver update needs a reboot, and containers that use the GPU may need the matching toolkit.", reboot: true,
	},
	{
		exact:  []string{"openssh-server", "openssh", "openssh-clients"},
		reason: "Restarts the SSH daemon. Open sessions stay connected; a broken new configuration would lock you out.",
	},
	{
		exact:    []string{"grub-pc", "grub-efi-amd64", "grub-efi-arm64", "grub2-common", "grub2-pc", "grub2-efi-x64", "shim", "shim-signed", "shim-x64"},
		prefixes: []string{"grub-", "grub2-", "shim-"},
		reason:   "Bootloader: a failed update can stop the server from booting.",
	},
	{
		exact:    []string{"redis-server", "mongodb-org", "mongodb-server", "mysql-server", "mariadb-server", "mariadb"},
		prefixes: []string{"postgresql-", "postgresql", "mysql-server-", "mariadb-server-", "mongodb-org-"},
		reason:   "A database server installed on the host itself restarts.",
	},
	{
		exact:  []string{"tailscale", "cloudflared", "wireguard", "wireguard-tools"},
		reason: "Restarts a network connector; the server may be unreachable for a moment.",
	},
}

func (r riskRule) matches(name string) bool {
	for _, e := range r.exact {
		if name == e {
			return true
		}
	}
	for _, p := range r.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Risks lists the packages among pkgs whose upgrade deserves a warning, in the
// order of pkgs. A package appears once, with the first matching reason.
func Risks(pkgs []agentprotocol.PatchPackage) []Risk {
	var out []Risk
	for _, p := range pkgs {
		for _, r := range riskRules {
			if r.matches(p.Name) {
				out = append(out, Risk{Package: p.Name, Reason: r.reason, Reboot: r.reboot})
				break
			}
		}
	}
	return out
}

// RebootLikely reports whether upgrading pkgs probably leaves the host wanting a reboot.
func RebootLikely(pkgs []agentprotocol.PatchPackage) bool {
	for _, r := range Risks(pkgs) {
		if r.Reboot {
			return true
		}
	}
	return false
}

// RestartsDocker reports whether upgrading pkgs restarts the Docker daemon.
func RestartsDocker(pkgs []agentprotocol.PatchPackage) bool {
	for _, r := range Risks(pkgs) {
		if strings.HasPrefix(r.Reason, "Restarts the Docker daemon") {
			return true
		}
	}
	return false
}
