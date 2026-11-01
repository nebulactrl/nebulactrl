//go:build windows

package main

import (
	"errors"
	"io"
)

// ttyState is the local terminal of an interactive session.
type ttyState struct {
	restore func()
	size    func() (cols, rows int)
	resizes <-chan [2]int
}

// openTTY is not available on Windows: `nebulactrl services shell` needs a
// POSIX terminal. Use the web terminal instead.
func openTTY(io.Reader, io.Writer) (*ttyState, error) {
	return nil, errors.New("`services shell` is not supported on Windows; use the web terminal")
}
