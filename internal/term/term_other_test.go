//go:build !windows

package term

import (
	"os"
	"path/filepath"
	"testing"
)

// Unix terminals understand ANSI escapes natively, so there's never a mode to switch on.
func TestEnableVTIsANoOp(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !enableVT(f) {
		t.Error("enableVT = false")
	}
}
