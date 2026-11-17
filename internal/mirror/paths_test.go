package mirror

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
)

// A file where a parent directory should be can't be cloned under. Every OS reports it the same
// way, and the file is left alone.
func TestFileInTheParentPath(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	blocker := filepath.Join(root, "acme")
	if err := os.WriteFile(blocker, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := f.task()
	task.Dir = filepath.Join(blocker, "team", "app")

	r := Sync(context.Background(), task)
	expect(t, r, Failed, CloneFailed)
	if r.Log == "" {
		t.Error("no explanation in the log")
	}
	if b, _ := os.ReadFile(blocker); string(b) != "mine" {
		t.Errorf("file was touched: %q", b)
	}
}

// A directory that exists but can't be listed isn't cloned over.
func TestUnreadableDirectory(t *testing.T) {
	f := newFixture(t)
	task := f.task()
	if err := os.MkdirAll(task.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	denyListing(t, task.Dir)

	r := Sync(context.Background(), task)
	expect(t, r, Failed, NotARepo)
	if r.Log == "" {
		t.Error("no explanation in the log")
	}
}

// denyListing stops this user listing dir until the test ends: an access-control entry on
// Windows, permission bits elsewhere.
func denyListing(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		u, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("icacls", dir, "/deny", u.Username+":(RD)").CombinedOutput(); err != nil {
			t.Skipf("icacls: %v\n%s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("icacls", dir, "/remove:d", u.Username).Run() })
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("root can list any directory")
	}
	if err := os.Chmod(dir, 0o300); err != nil { // writable and searchable, not listable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
