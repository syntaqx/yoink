//go:build !windows

// Package term detects interactive terminals.
package term

import "os"

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// EnableColor reports whether f is a terminal that can show ANSI colors.
func EnableColor(f *os.File) bool {
	return IsTerminal(f) && os.Getenv("TERM") != "dumb"
}
