package stats

import (
	"os"
	"strings"
)

// tunPath is the TUN device mesh link nodes (kernel tailscale) need; tests
// replace it.
var tunPath = "/dev/net/tun"

// TunDevice reports whether /dev/net/tun exists on this host. An agent that
// runs in a container does not see the host's /dev: NEBULACTRL_TUN_DEVICE=1 or
// =0 states the answer for the daemon's host explicitly.
func TunDevice() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NEBULACTRL_TUN_DEVICE"))) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	st, err := os.Stat(tunPath)
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
