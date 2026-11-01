package stats

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// procSys is where the kernel parameters are readable (a variable for tests).
var procSys = "/proc/sys"

// hostSysctls reads the host kernel parameters services may declare as
// requirements (agentprotocol.HostSysctlKeys). A key that cannot be read
// (not Linux, no /proc/sys, not a number) is left out.
func hostSysctls() map[string]int64 {
	out := map[string]int64{}
	for _, key := range agentprotocol.HostSysctlKeys {
		raw, err := os.ReadFile(filepath.Join(procSys, strings.ReplaceAll(key, ".", "/")))
		if err != nil {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			out[key] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
