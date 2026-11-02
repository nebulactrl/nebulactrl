//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// prGetDumpable is PR_GET_DUMPABLE from <linux/prctl.h>.
const prGetDumpable = 3

// TestHardenProcessDisablesCoreDumpsAndDumpability runs hardenProcess in a
// child process: it lowers limits of the whole process, which must not leak
// into the other tests.
func TestHardenProcessDisablesCoreDumpsAndDumpability(t *testing.T) {
	if os.Getenv("NEBULACTRL_TEST_HARDEN") == "1" {
		// Raise the soft core limit to the hard one so the change is observable
		// wherever the host allows it (an unprivileged process cannot raise the hard limit).
		var before syscall.Rlimit
		require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_CORE, &before))
		before.Cur = before.Max
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_CORE, &before))
		dumpable, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prGetDumpable, 0, 0)
		require.Zero(t, errno)
		require.Equal(t, uintptr(1), dumpable, "the child starts dumpable")

		require.NoError(t, hardenProcess())

		var lim syscall.Rlimit
		require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_CORE, &lim))
		require.Zero(t, lim.Cur)
		require.Zero(t, lim.Max)
		dumpable, _, errno = syscall.RawSyscall(syscall.SYS_PRCTL, prGetDumpable, 0, 0)
		require.Zero(t, errno)
		require.Zero(t, dumpable)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.v", "-test.run=^TestHardenProcessDisablesCoreDumpsAndDumpability$")
	cmd.Env = append(os.Environ(), "NEBULACTRL_TEST_HARDEN=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Run(), strings.TrimSpace(out.String()))
	require.Contains(t, out.String(), "--- PASS: TestHardenProcessDisablesCoreDumpsAndDumpability", "the child must really run the check")
}
