//go:build unix

package main

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// termWidth returns the terminal column count. Best effort: ioctl TIOCGWINSZ,
// then $COLUMNS, then 80.
func termWidth() int {
	ws := struct{ Row, Col, Xpix, Ypix uint16 }{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		return c
	}
	return 80
}
