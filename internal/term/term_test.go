package term

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesAndPipesAreNotTerminals(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	for name, file := range map[string]*os.File{"file": f, "pipe read end": r, "pipe write end": w} {
		if IsTerminal(file) {
			t.Errorf("%s: IsTerminal = true", name)
		}
		if EnableColor(file) {
			t.Errorf("%s: EnableColor = true", name)
		}
	}
}

func TestDumbTerminalHasNoColor(t *testing.T) {
	t.Setenv("TERM", "dumb")
	// Whatever stdout is attached to, TERM=dumb wins.
	if EnableColor(os.Stdout) {
		t.Error("EnableColor = true with TERM=dumb")
	}
}
