//go:build windows

// Package term detects interactive terminals.
package term

import (
	"os"

	"golang.org/x/sys/windows"
)

func consoleMode(f *os.File) (uint32, bool) {
	var mode uint32
	err := windows.GetConsoleMode(windows.Handle(f.Fd()), &mode)
	return mode, err == nil
}

// IsTerminal reports whether f is an interactive console.
func IsTerminal(f *os.File) bool {
	_, ok := consoleMode(f)
	return ok
}

// EnableColor turns on ANSI escape handling for the console f and reports whether it worked.
func EnableColor(f *os.File) bool {
	mode, ok := consoleMode(f)
	if !ok {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(windows.Handle(f.Fd()), mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
