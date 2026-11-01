//go:build darwin

package hostpty

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// openPTY opens a master/slave pair through /dev/ptmx (grantpt, unlockpt and
// ptsname are ioctls on macOS).
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = m.Close()
		}
	}()
	// TIOCPTYGNAME takes a 128-byte buffer (IOCPARM_LEN of the request).
	name := make([]byte, 128)
	if err := ioctl(m, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil { //nolint:gosec // ioctl needs a pointer to the name buffer
		return nil, nil, err
	}
	end := 0
	for end < len(name) && name[end] != 0 {
		end++
	}
	if end == len(name) {
		return nil, nil, errors.New("hostpty: TIOCPTYGNAME returned an unterminated name")
	}
	if err := ioctl(m, syscall.TIOCPTYGRANT, nil); err != nil {
		return nil, nil, err
	}
	if err := ioctl(m, syscall.TIOCPTYUNLK, nil); err != nil {
		return nil, nil, err
	}
	s, err := os.OpenFile(string(name[:end]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	return m, s, nil
}
