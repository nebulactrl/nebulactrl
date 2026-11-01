package stats

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestPlatformsFromBinfmtListsOnlyForeignPlatforms(t *testing.T) {
	require.Equal(t, []string{"linux/arm/v6", "linux/arm/v7", "linux/arm64", "linux/riscv64"},
		PlatformsFromBinfmt([]string{"qemu-aarch64", "qemu-x86_64", "qemu-arm", "qemu-riscv64", "qemu-i386"}, "amd64"),
		"amd64 runs amd64 and 386 natively")
	require.Equal(t, []string{"linux/386", "linux/amd64"},
		PlatformsFromBinfmt([]string{"qemu-x86_64", "qemu-aarch64", "qemu-arm", "qemu-i386"}, "arm64"),
		"arm64 runs arm64 and arm natively")
	require.Empty(t, PlatformsFromBinfmt([]string{"python3.12", "register"}, "amd64"), "only qemu entries count")
	require.Empty(t, PlatformsFromBinfmt(nil, "amd64"))
}

func TestEnabledBinfmtReadsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("qemu-aarch64", "enabled\ninterpreter /usr/bin/qemu-aarch64\nflags: F\n")
	write("qemu-arm", "disabled\n")
	write("qemu-riscv64", "enabled\n")
	write("register", "")
	write("status", "enabled\n")
	got := enabledBinfmt(dir)
	slices.Sort(got)
	require.Equal(t, []string{"qemu-aarch64", "qemu-riscv64"}, got)
	require.Empty(t, enabledBinfmt(filepath.Join(dir, "missing")))
}

func TestFactsAdvertiseBuildPushAndEmulationOnlyWithBuildx(t *testing.T) {
	probes := 0
	c := New(Options{
		HTTPPort: 18080, HTTPSPort: 18443,
		BuildxVersion: func(context.Context) (string, error) { probes++; return "github.com/docker/buildx v0.40.0", nil },
		Emulation:     func(context.Context) ([]string, bool) { return []string{"linux/arm64"}, true },
	})
	f := c.Facts(context.Background())
	require.Contains(t, f.Capabilities, agentprotocol.CapBuildPush)
	require.Equal(t, []string{"linux/arm64"}, f.Emulation)
	c.Facts(context.Background())
	require.Equal(t, 1, probes, "the plugin is probed once")

	for name, opt := range map[string]Options{
		"no probe": {},
		"plugin absent": {BuildxVersion: func(context.Context) (string, error) {
			return "", errors.New("docker: 'buildx' is not a docker command")
		}},
	} {
		opt.HTTPPort, opt.HTTPSPort = 18080, 18443
		opt.Emulation = func(context.Context) ([]string, bool) { return []string{"linux/arm64"}, true }
		f := New(opt).Facts(context.Background())
		require.NotContains(t, f.Capabilities, agentprotocol.CapBuildPush, name)
		require.Empty(t, f.Emulation, name)
	}
}

func TestEmulationIsCachedOnlyOnceKnown(t *testing.T) {
	calls := 0
	answers := []struct {
		platforms []string
		final     bool
	}{{nil, false}, {[]string{"linux/amd64"}, true}}
	c := New(Options{
		HTTPPort: 18080, HTTPSPort: 18443,
		BuildxVersion: func(context.Context) (string, error) { return "v", nil },
		Emulation: func(context.Context) ([]string, bool) {
			a := answers[min(calls, len(answers)-1)]
			calls++
			return a.platforms, a.final
		},
	})
	require.Empty(t, c.Facts(context.Background()).Emulation, "first probe was not conclusive")
	require.Equal(t, []string{"linux/amd64"}, c.Facts(context.Background()).Emulation)
	require.Equal(t, []string{"linux/amd64"}, c.Facts(context.Background()).Emulation)
	require.Equal(t, 2, calls, "a conclusive answer is not asked again")
}
