package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloneFailureLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	task := f.task()
	task.URL = filepath.Join(t.TempDir(), "missing.git")

	r := Sync(context.Background(), task)
	expect(t, r, Failed, CloneFailed)
	if r.Log == "" {
		t.Error("no git output to show with -v")
	}
	// A failed clone must not leave a directory that the next run mistakes for a clone.
	if _, err := os.Stat(task.Dir); err == nil {
		t.Errorf("%s exists after a failed clone", task.Dir)
	}
}

func TestCloneIntoEmptyDirectory(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	if err := os.MkdirAll(task.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	expect(t, Sync(context.Background(), task), Cloned, "")
	if b, _ := os.ReadFile(filepath.Join(task.Dir, "a.txt")); string(b) != "one" {
		t.Errorf("a.txt = %q", b)
	}
}

func TestDirIsAFile(t *testing.T) {
	f := newFixture(t)
	task := f.task()
	if err := os.MkdirAll(filepath.Dir(task.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task.Dir, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect(t, Sync(context.Background(), task), Failed, NotARepo)
	if b, _ := os.ReadFile(task.Dir); string(b) != "mine" {
		t.Errorf("file was touched: %q", b)
	}
}

func TestFetchFailure(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")
	head := run(t, task.Dir, "rev-parse", "HEAD")

	if err := os.RemoveAll(f.remote); err != nil {
		t.Fatal(err)
	}
	r := Sync(ctx, task)
	expect(t, r, Failed, FetchFailed)
	if r.Log == "" {
		t.Error("no git output to show with -v")
	}
	if got := run(t, task.Dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
}

func TestCheckoutBlockedByLocalChanges(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")

	// On a feature branch whose a.txt differs from main's, with an uncommitted edit to it: the
	// switch back to main would overwrite the edit, so git refuses and the edit is blamed.
	run(t, task.Dir, "checkout", "--quiet", "-b", "feature")
	if err := os.WriteFile(filepath.Join(task.Dir, "a.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, task.Dir, "commit", "--quiet", "-am", "feature")
	if err := os.WriteFile(filepath.Join(task.Dir, "a.txt"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	expect(t, Sync(ctx, task), Failed, Dirty)
	if head := run(t, task.Dir, "symbolic-ref", "--short", "HEAD"); head != "feature" {
		t.Errorf("HEAD = %q, want feature", head)
	}
	if b, _ := os.ReadFile(filepath.Join(task.Dir, "a.txt")); string(b) != "local edit" {
		t.Errorf("local edit was touched: %q", b)
	}
}

func TestResetOutsideARepository(t *testing.T) {
	newFixture(t)
	task := Task{Name: "acme/repo", Dir: t.TempDir(), URL: "unused", Branch: "main"}
	r := Reset(context.Background(), task)
	expect(t, r, Failed, ResetFailed)
	if !strings.Contains(strings.ToLower(r.Log), "not a git repository") {
		t.Errorf("log = %q", r.Log)
	}
}

func TestResetDiscardsOnlyTrackedChanges(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")

	untracked := filepath.Join(task.Dir, "notes.txt")
	for path, content := range map[string]string{filepath.Join(task.Dir, "a.txt"): "edit", untracked: "keep me"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, Reset(ctx, task), Current, "")
	if b, _ := os.ReadFile(filepath.Join(task.Dir, "a.txt")); string(b) != "one" {
		t.Errorf("a.txt = %q, want the committed content", b)
	}
	if b, _ := os.ReadFile(untracked); string(b) != "keep me" {
		t.Errorf("untracked file = %q, want it kept", b)
	}
}

func TestInterruptedSyncs(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	missing := f.task()
	r := Sync(ctx, missing)
	expect(t, r, Failed, CloneFailed)
	if r.Log != "interrupted" {
		t.Errorf("clone log = %q, want interrupted", r.Log)
	}

	existing := f.task()
	expect(t, Sync(context.Background(), existing), Cloned, "")
	r = Sync(ctx, existing)
	expect(t, r, Failed, FetchFailed)
	if r.Log != "interrupted" {
		t.Errorf("fetch log = %q, want interrupted", r.Log)
	}
}
