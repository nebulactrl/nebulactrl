package stats

import (
	"os"
	"runtime"
)

// systemdRunDir exists while systemd is the host's init (sd_booted(3)).
const systemdRunDir = "/run/systemd/system"

// systemdHost reports whether the agent sees a Linux host that runs systemd: the
// host can start the installer as a transient unit that outlives the agent
// (agentprotocol.CapUpdateControlPlane).
func systemdHost() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	st, err := os.Stat(systemdRunDir)
	return err == nil && st.IsDir()
}
