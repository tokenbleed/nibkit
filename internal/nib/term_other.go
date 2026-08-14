//go:build !unix

package nib

import (
	"os"
	"strconv"
)

func termWidth() int {
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		return c
	}
	return 80
}

func isTTY(fd uintptr) bool { return false }
