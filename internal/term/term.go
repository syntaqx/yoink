// Package term detects interactive terminals, including the pipe-based ones that mintty (Git
// Bash) and Cygwin present on Windows.
package term

import (
	"os"

	"github.com/mattn/go-isatty"
)

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// EnableColor reports whether f is a terminal that shows ANSI colors, turning on escape-sequence
// handling first where the console needs it (Windows).
func EnableColor(f *os.File) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fd := f.Fd()
	if isatty.IsCygwinTerminal(fd) {
		return true // mintty and Cygwin terminals understand ANSI natively
	}
	return isatty.IsTerminal(fd) && enableVT(f)
}
