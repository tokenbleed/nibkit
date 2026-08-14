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
	w := winszCols(os.Stdout.Fd())
	if w > 0 {
		return w
	}
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		return c
	}
	return 80
}

// isTTY reports whether fd is a real terminal. TIOCGWINSZ fails with ENOTTY on
// pipes, files, and /dev/null, so this is a true isatty, unlike fstat char-device checks.
func isTTY(fd uintptr) bool { return winszCols(fd) > 0 }

func winszCols(fd uintptr) int {
	ws := struct{ Row, Col, Xpix, Ypix uint16 }{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	return 0
}
