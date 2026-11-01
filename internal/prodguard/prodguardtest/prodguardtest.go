// Package prodguardtest resets the process-wide production guard between tests.
package prodguardtest

import "github.com/nebulactrl/nebulactrl/internal/prodguard"

// Uninstall removes the installed Guard, so prodguard.Check refuses guarded
// calls again. Tests that install their own Guard register it with t.Cleanup.
func Uninstall() { (*prodguard.Guard)(nil).Install() }
