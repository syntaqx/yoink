//go:build windows

// Package term detects interactive terminals.
package term

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode = kernel32.NewProc("GetConsoleMode")
	setConsoleMode = kernel32.NewProc("SetConsoleMode")
)

const enableVirtualTerminalProcessing = 0x0004

func consoleMode(f *os.File) (uint32, bool) {
	var mode uint32
	r, _, _ := getConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	return mode, r != 0
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
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := setConsoleMode.Call(f.Fd(), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
