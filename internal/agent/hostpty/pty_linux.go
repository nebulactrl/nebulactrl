//go:build linux

package hostpty

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// openPTY opens a master/slave pair through /dev/ptmx.
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
	var n uint32
	if err = ioctl(m, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		return nil, nil, err
	}
	var unlock int32 // TIOCSPTLCK with a pointer to zero clears the lock
	if err = ioctl(m, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return nil, nil, err
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	return m, s, nil
}
