package nib

import "os"

// Version is the nibkit release version.
const Version = "1.5.1"

// IsTerminal reports whether the file descriptor is a character device
// (a real terminal rather than a pipe or file).
func IsTerminal() bool {
	return isTTY(os.Stdout.Fd()) && isTTY(os.Stdin.Fd())
}
