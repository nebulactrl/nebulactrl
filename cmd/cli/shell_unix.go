//go:build !windows

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// ttyState is the local terminal of an interactive session.
type ttyState struct {
	// restore puts the terminal back (safe to call more than once).
	restore func()
	// size returns the terminal's columns and rows.
	size    func() (cols, rows int)
	resizes <-chan [2]int
}

func isCharDevice(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func stty(in *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...) //nolint:gosec // fixed binary, flags chosen by this package
	cmd.Stdin = in
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// openTTY puts the terminal into raw mode (every key reaches the remote shell,
// Ctrl-C included) through stty, and reports window size changes (SIGWINCH).
// Both streams must be a terminal.
func openTTY(in io.Reader, out io.Writer) (*ttyState, error) {
	inf, ok := in.(*os.File)
	outf, ok2 := out.(*os.File)
	if !ok || !ok2 || !isCharDevice(inf) || !isCharDevice(outf) {
		return nil, errors.New("a shell needs an interactive terminal on stdin and stdout")
	}
	saved, err := stty(inf, "-g")
	if err != nil {
		return nil, errors.New("could not read the terminal settings (is `stty` installed?)")
	}
	if _, err := stty(inf, "raw", "-echo"); err != nil {
		return nil, errors.New("could not switch the terminal to raw mode")
	}
	restore := func() { _, _ = stty(inf, saved) }
	size := func() (int, int) {
		s, err := stty(inf, "size")
		if err != nil {
			return 80, 24
		}
		f := strings.Fields(s)
		if len(f) != 2 {
			return 80, 24
		}
		rows, _ := strconv.Atoi(f[0])
		cols, _ := strconv.Atoi(f[1])
		if rows <= 0 || cols <= 0 {
			return 80, 24
		}
		return cols, rows
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	resizes := make(chan [2]int, 1)
	go func() {
		for range winch {
			c, r := size()
			select {
			case resizes <- [2]int{c, r}:
			default:
			}
		}
	}()
	restoreAll := func() {
		signal.Stop(winch)
		restore()
	}
	return &ttyState{restore: restoreAll, size: size, resizes: resizes}, nil
}
