package mirror

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a bare "server" repository plus a separate working clone used to push to it.
type fixture struct {
	t      *testing.T
	remote string // bare repository, used as the clone URL
	pusher string // working copy that commits and pushes to remote
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Isolate from the developer's git config (signing, hooks, default branch...).
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")

	base := t.TempDir()
	f := &fixture{t: t, remote: filepath.Join(base, "remote.git"), pusher: filepath.Join(base, "pusher")}
	run(t, base, "init", "--quiet", "--bare", "--initial-branch=main", f.remote)
	run(t, base, "clone", "--quiet", f.remote, f.pusher)
	return f
}

// commit writes file=content in the pusher and pushes it to main.
func (f *fixture) commit(file, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.pusher, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	run(f.t, f.pusher, "add", file)
	run(f.t, f.pusher, "commit", "--quiet", "-m", "change "+file)
	run(f.t, f.pusher, "push", "--quiet", "origin", "HEAD:main")
}

func (f *fixture) task() Task {
	return Task{Name: "acme/repo", Dir: filepath.Join(f.t.TempDir(), "acme", "repo"), URL: f.remote, Branch: "main"}
}

func expect(t *testing.T, r Result, status Status, reason Reason) {
	t.Helper()
	if r.Status != status || r.Reason != reason {
		t.Fatalf("got status %d reason %q (note %q, log %q); want status %d reason %q",
			r.Status, r.Reason, r.Note, r.Log, status, reason)
	}
}

func TestCloneThenCurrentThenUpdated(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()

	expect(t, Sync(ctx, task), Cloned, "")
	expect(t, Sync(ctx, task), Current, "")

	f.commit("a.txt", "two")
	f.commit("b.txt", "three")
	r := Sync(ctx, task)
	expect(t, r, Updated, "")
	if r.Note != "2 new commits" {
		t.Errorf("note = %q, want %q", r.Note, "2 new commits")
	}
}

func TestSwitchesBackToDefaultBranch(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")
	run(t, task.Dir, "checkout", "--quiet", "-b", "feature")

	f.commit("a.txt", "two")
	expect(t, Sync(ctx, task), Updated, "")
	if head := run(t, task.Dir, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Errorf("HEAD = %q, want main", head)
	}
}

func TestDirtyConflictIsLeftAloneThenReset(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")

	if err := os.WriteFile(filepath.Join(task.Dir, "a.txt"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit("a.txt", "two")
	expect(t, Sync(ctx, task), Failed, Dirty)
	if b, _ := os.ReadFile(filepath.Join(task.Dir, "a.txt")); string(b) != "local edit" {
		t.Fatalf("local edit was touched: %q", b)
	}

	expect(t, Reset(ctx, task), Updated, "")
	if b, _ := os.ReadFile(filepath.Join(task.Dir, "a.txt")); string(b) != "two" {
		t.Fatalf("after reset a.txt = %q, want %q", b, "two")
	}
}

func TestDivergedNeverMerges(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")

	if err := os.WriteFile(filepath.Join(task.Dir, "local.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, task.Dir, "add", "local.txt")
	run(t, task.Dir, "commit", "--quiet", "-m", "local")
	local := run(t, task.Dir, "rev-parse", "HEAD")
	f.commit("a.txt", "two")

	expect(t, Sync(ctx, task), Failed, Diverged)
	if head := run(t, task.Dir, "rev-parse", "HEAD"); head != local {
		t.Errorf("HEAD moved from %s to %s", local, head)
	}
}

func TestEmptyRepository(t *testing.T) {
	f := newFixture(t)
	task := f.task()
	ctx := context.Background()

	r := Sync(ctx, task)
	expect(t, r, Cloned, "")
	if r.Note != "empty" {
		t.Errorf("note = %q, want empty", r.Note)
	}
	r = Sync(ctx, task)
	expect(t, r, Current, "")

	// The first push to an empty repository shows up as new commits.
	f.commit("a.txt", "one")
	r = Sync(ctx, task)
	expect(t, r, Updated, "")
	if r.Note != "1 new commit" {
		t.Errorf("note = %q, want %q", r.Note, "1 new commit")
	}
}

func TestExistingNonRepoDirectory(t *testing.T) {
	f := newFixture(t)
	task := f.task()
	if err := os.MkdirAll(task.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.Dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect(t, Sync(context.Background(), task), Failed, NotARepo)
}

func TestRewritesOtherProtocolRemote(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one")
	task := f.task()
	ctx := context.Background()
	expect(t, Sync(ctx, task), Cloned, "")

	// Pretend the clone was made over the "other" protocol; the sync should repoint it.
	task.OtherURL = "https://example.com/acme/repo.git"
	run(t, task.Dir, "remote", "set-url", "origin", "https://EXAMPLE.com/acme/repo")
	// The fetch fails (example.com isn't a remote), but the rewrite happens first.
	Sync(ctx, task)
	if got := run(t, task.Dir, "remote", "get-url", "origin"); got != task.URL {
		t.Errorf("origin = %q, want %q", got, task.URL)
	}
}

func TestRunVisitsEveryTask(t *testing.T) {
	var tasks []Task
	for i := range 25 {
		tasks = append(tasks, Task{Name: string(rune('a' + i))})
	}
	seen := map[string]bool{}
	Run(context.Background(), tasks, 4, func(_ context.Context, t Task) Result {
		return Result{Task: t}
	}, func(r Result) { seen[r.Task.Name] = true })
	if len(seen) != len(tasks) {
		t.Fatalf("visited %d of %d tasks", len(seen), len(tasks))
	}
}

func TestSameRemote(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"https://github.com/acme/repo.git", "https://github.com/Acme/Repo", true},
		{"https://contoso@dev.azure.com/contoso/P/_git/r", "https://dev.azure.com/contoso/P/_git/r", true},
		{"https://dev.azure.com/c/My%20Project/_git/r", "https://dev.azure.com/c/My%20Project/_git/r/", true},
		{"git@github.com:acme/repo.git", "git@github.com:acme/repo", true},
		{"git@github.com:acme/repo.git", "https://github.com/acme/repo.git", false},
		{"https://github.com/acme/repo", "https://github.com/acme/other", false},
	} {
		if got := sameRemote(c.a, c.b); got != c.want {
			t.Errorf("sameRemote(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
