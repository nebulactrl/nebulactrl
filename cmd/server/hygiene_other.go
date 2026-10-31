//go:build unix && !linux

package main

import "syscall"

// hardenProcess disables core dumps (RLIMIT_CORE=0). PR_SET_DUMPABLE is Linux
// only; the production control plane runs on Linux.
func hardenProcess() error {
	return syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
}
