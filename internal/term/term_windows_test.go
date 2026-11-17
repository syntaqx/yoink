//go:build windows

package term

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// console opens this process's console screen buffer, skipping the test if there isn't one
// (a detached process, or a service).
func console(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no console attached: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func consoleMode(t *testing.T, f *os.File) uint32 {
	t.Helper()
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(f.Fd()), &mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func setConsoleMode(t *testing.T, f *os.File, mode uint32) {
	t.Helper()
	if err := windows.SetConsoleMode(windows.Handle(f.Fd()), mode); err != nil {
		t.Fatal(err)
	}
}

func TestEnableVTTurnsOnEscapeHandling(t *testing.T) {
	f := console(t)
	original := consoleMode(t, f)
	t.Cleanup(func() { setConsoleMode(t, f, original) })

	// Start from an old-style console with escape handling off.
	setConsoleMode(t, f, original&^windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	if !enableVT(f) {
		t.Fatal("enableVT = false on a console")
	}
	if consoleMode(t, f)&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING == 0 {
		t.Error("enableVT reported success but left escape handling off")
	}

	// Already on: nothing to change, still true.
	before := consoleMode(t, f)
	if !enableVT(f) {
		t.Error("enableVT = false when escape handling was already on")
	}
	if after := consoleMode(t, f); after != before {
		t.Errorf("console mode changed from %#x to %#x", before, after)
	}
}

func TestConsoleIsAColorTerminal(t *testing.T) {
	f := console(t)
	t.Setenv("TERM", "")
	if !IsTerminal(f) {
		t.Error("IsTerminal = false for the console")
	}
	if !EnableColor(f) {
		t.Error("EnableColor = false for the console")
	}
}

func TestEnableVTOnAFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if enableVT(f) {
		t.Error("enableVT = true for a regular file")
	}
}

// mintty (Git Bash) and Cygwin give programs a named pipe, not a console. They're recognized by
// the pipe's name, and get color without any console mode changes.
func TestMinttyPipeIsAColorTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
	}{
		{"msys-%s-pty0-to-master", true},
		{"cygwin-%s-pty3-from-master", true},
		{"yoink-test-%s", false}, // an ordinary pipe, such as output piped to another program
	} {
		f := namedPipe(t, tc.name)
		if got := IsTerminal(f); got != tc.terminal {
			t.Errorf("%s: IsTerminal = %v, want %v", tc.name, got, tc.terminal)
		}
		if got := EnableColor(f); got != tc.terminal {
			t.Errorf("%s: EnableColor = %v, want %v", tc.name, got, tc.terminal)
		}
	}
}

// namedPipe creates a pipe named after pattern (with %s replaced by a random token) and returns
// the client end.
func namedPipe(t *testing.T, pattern string) *os.File {
	t.Helper()
	t.Setenv("TERM", "")
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	path := `\\.\pipe\` + fmt.Sprintf(pattern, hex.EncodeToString(token))
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	server, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	t.Cleanup(func() { windows.CloseHandle(server) })

	client, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}
