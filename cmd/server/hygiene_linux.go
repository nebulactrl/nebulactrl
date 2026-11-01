//go:build linux

package main

import (
	"syscall"
)

// prSetDumpable is PR_SET_DUMPABLE from <linux/prctl.h>.
const prSetDumpable = 4

// hardenProcess keeps key material out of core dumps and away from ptrace and
// /proc/<pid>/mem of same-uid processes: RLIMIT_CORE=0 and PR_SET_DUMPABLE=0.
// It returns the first failure; the server still runs without the protection.
func hardenProcess() error {
	var first error
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		first = err
	}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 && first == nil {
		first = errno
	}
	return first
}
