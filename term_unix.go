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
	if c, ok := winsz(os.Stdout.Fd()); ok && c > 0 {
		return c
	}
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		return c
	}
	return 80
}

// isTTY reports whether fd is a real terminal: TIOCGWINSZ fails with ENOTTY on
// pipes, files, and /dev/null (a char device), so errno==0 is a true isatty even
// when the winsize is still 0x0 (fresh pty without a size set).
func isTTY(fd uintptr) bool {
	_, ok := winsz(fd)
	return ok
}

func winsz(fd uintptr) (cols int, ok bool) {
	ws := struct{ Row, Col, Xpix, Ypix uint16 }{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0, false
	}
	return int(ws.Col), true
}
