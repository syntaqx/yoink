package ignore

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only a missing file is fine. One that exists but can't be read stops the run rather than
// syncing repositories the user meant to skip.
func TestUnreadableIgnoreFile(t *testing.T) {
	t.Run("can't be opened", func(t *testing.T) {
		if _, err := Load([]string{"bad\x00name"}, nil); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("can't be read", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), ".yoinkignore")
		long := "acme/" + strings.Repeat("x", bufio.MaxScanTokenSize) + "\n"
		if err := os.WriteFile(file, []byte("acme/ok\n"+long), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load([]string{file}, nil)
		if !errors.Is(err, bufio.ErrTooLong) || !strings.Contains(err.Error(), file) {
			t.Errorf("err = %v, want a read error naming %s", err, file)
		}
	})
}
