package stats

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostSysctlsReadsTheDeclarableKeys(t *testing.T) {
	root := t.TempDir()
	write := func(rel, v string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(v), 0o644))
	}
	write("vm/max_map_count", "65530\n")
	write("fs/inotify/max_user_watches", "8192\n")
	write("fs/file-max", "not a number\n")
	old := procSys
	procSys = root
	t.Cleanup(func() { procSys = old })

	require.Equal(t, map[string]int64{"vm.max_map_count": 65530, "fs.inotify.max_user_watches": 8192}, hostSysctls(),
		"unreadable and non-numeric keys are left out")

	procSys = filepath.Join(root, "missing")
	require.Nil(t, hostSysctls())
}
