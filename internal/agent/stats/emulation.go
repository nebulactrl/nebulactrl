package stats

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Cross-architecture builds need the kernel's binfmt_misc to run foreign
// binaries through QEMU user-mode emulation (registered by the distribution's
// qemu-user-static, or by `tonistiigi/binfmt --install all`). The agent reports
// the platforms that work in Facts.Emulation; the control plane only picks a
// build server for another architecture when its owner allowed emulation.

const binfmtDir = "/proc/sys/fs/binfmt_misc"

// binfmtPlatforms maps a binfmt_misc qemu entry to the platforms it runs.
var binfmtPlatforms = map[string][]string{
	"qemu-x86_64":      {"linux/amd64"},
	"qemu-aarch64":     {"linux/arm64"},
	"qemu-arm":         {"linux/arm/v7", "linux/arm/v6"},
	"qemu-riscv64":     {"linux/riscv64"},
	"qemu-ppc64le":     {"linux/ppc64le"},
	"qemu-s390x":       {"linux/s390x"},
	"qemu-loongarch64": {"linux/loong64"},
	"qemu-i386":        {"linux/386"},
	"qemu-mips64el":    {"linux/mips64le"},
	"qemu-mips64":      {"linux/mips64"},
}

// nativePlatforms are what the host runs without emulation.
func nativePlatforms(goarch string) map[string]bool {
	switch goarch {
	case "amd64":
		return map[string]bool{"linux/amd64": true, "linux/386": true}
	case "arm64":
		return map[string]bool{"linux/arm64": true, "linux/arm/v7": true, "linux/arm/v6": true}
	}
	return map[string]bool{"linux/" + goarch: true}
}

// PlatformsFromBinfmt turns the enabled binfmt_misc entry names into the sorted
// list of foreign platforms they provide on a host of architecture goarch.
func PlatformsFromBinfmt(enabled []string, goarch string) []string {
	native := nativePlatforms(goarch)
	seen := map[string]bool{}
	var out []string
	for _, name := range enabled {
		for _, p := range binfmtPlatforms[name] {
			if !native[p] && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// enabledBinfmt lists the qemu entries of a binfmt_misc directory that are
// enabled (their first line reads "enabled").
func enabledBinfmt(dir string) []string {
	files, err := filepath.Glob(filepath.Join(dir, "qemu-*"))
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if first, _, _ := strings.Cut(string(raw), "\n"); strings.TrimSpace(first) == "enabled" {
			out = append(out, filepath.Base(f))
		}
	}
	return out
}

// emulationScript lists the enabled qemu entries of the host's binfmt_misc as
// "<name>" lines.
const emulationScript = `for f in ` + binfmtDir + `/qemu-*; do [ -r "$f" ] && [ "$(head -n 1 "$f")" = enabled ] && basename "$f"; done; true`

// emulation returns the foreign platforms this host can run. An agent on the
// bare host reads binfmt_misc itself; an agent in a container usually does not
// see the host's entries, so it asks a one-shot privileged helper that enters
// the host's namespaces (the mechanism of the host shell and hardening). The
// answer is cached once it is known.
func (c *Collector) emulation(ctx context.Context) []string {
	c.emuMu.Lock()
	defer c.emuMu.Unlock()
	if c.emuDone {
		return c.emu
	}
	var (
		got []string
		ok  = true
	)
	switch {
	case c.opt.Emulation != nil:
		got, ok = c.opt.Emulation(ctx)
	default:
		names := enabledBinfmt(binfmtDir)
		if len(names) == 0 && inContainer() {
			names, ok = c.hostBinfmt(ctx)
		}
		got = PlatformsFromBinfmt(names, runtime.GOARCH)
	}
	if ok {
		c.emu, c.emuDone = got, true
	}
	return got
}

func inContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// hostBinfmt lists the host's enabled qemu entries through a helper container.
func (c *Collector) hostBinfmt(ctx context.Context) ([]string, bool) {
	runner, ok := c.opt.Engine.(docker.HostCommandRunner)
	if !ok {
		return nil, true
	}
	self := c.opt.AgentContainer
	if self == "" {
		self, _ = os.Hostname()
	}
	if self == "" {
		return nil, true
	}
	hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	info, err := c.opt.Engine.ContainerInspect(hctx, self)
	if err != nil {
		return nil, true // not a container we can find: nothing to ask
	}
	image := info.ImageID
	if image == "" {
		image = info.Image
	}
	if image == "" {
		return nil, true
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	res, err := runner.RunHostCommand(hctx, docker.HostCommandSpec{
		Name:   "nebulactrl-hostcmd-binfmt-" + hex.EncodeToString(b[:]),
		Image:  image,
		Argv:   []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", "/bin/sh", "-c", emulationScript},
		Labels: map[string]string{agentprotocol.LabelManaged: "hostcmd"},
	})
	if err != nil {
		return nil, false // try again at the next hello
	}
	var names []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "qemu-") {
			names = append(names, l)
		}
	}
	return names, true
}
