// Package mirror clones repositories that are missing and fast-forwards the ones that exist. It
// knows nothing about providers: a Task is just a directory, a URL and a default branch.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Task is one repository to bring up to date.
type Task struct {
	Name     string // display name, e.g. "owner/repo"
	Dir      string // absolute path of the clone
	URL      string // clone URL
	OtherURL string // the same repository over the other protocol; an origin using it is rewritten to URL
	Branch   string // default branch; "" for a repository with no commits
}

// Status is the outcome of syncing a Task.
type Status int

const (
	Current Status = iota // already up to date
	Updated               // fast-forwarded to new commits
	Cloned                // newly cloned
	Failed
)

// Reason says why a Task failed.
type Reason string

const (
	Dirty          Reason = "local changes"
	Diverged       Reason = "diverged"
	FetchFailed    Reason = "fetch failed"
	CheckoutFailed Reason = "checkout failed"
	CloneFailed    Reason = "clone failed"
	ResetFailed    Reason = "reset failed"
	NotARepo       Reason = "exists, not a git repository"
)

// Result is what happened to a Task.
type Result struct {
	Task   Task
	Status Status
	Reason Reason // set when Status is Failed
	Note   string // detail worth showing, e.g. "3 new commits" or "empty"
	Log    string // git's output from the step that failed
}

// Work is a function that brings one Task up to date, such as Sync or Reset.
type Work func(context.Context, Task) Result

// Run performs work on every task with at most jobs running at once, calling done once per task,
// serially, as each finishes.
func Run(ctx context.Context, tasks []Task, jobs int, work Work, done func(Result)) {
	queue := make(chan Task)
	results := make(chan Result)
	var wg sync.WaitGroup
	for range max(jobs, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range queue {
				results <- work(ctx, t)
			}
		}()
	}
	go func() {
		defer close(results)
		defer wg.Wait()
		defer close(queue)
		for _, t := range tasks {
			select {
			case queue <- t:
			case <-ctx.Done():
				return
			}
		}
	}()
	for r := range results {
		done(r)
	}
}

// Sync clones the repository if it's missing, otherwise checks out the default branch and
// fast-forwards it. It never merges, rebases or discards anything.
func Sync(ctx context.Context, t Task) Result {
	if _, err := os.Stat(filepath.Join(t.Dir, ".git")); err == nil {
		return update(ctx, t)
	}
	return clone(ctx, t)
}

// Reset discards uncommitted changes to tracked files (git reset --hard), then syncs.
func Reset(ctx context.Context, t Task) Result {
	if out, err := git(ctx, t.Dir, "reset", "--hard", "--quiet", "HEAD"); err != nil {
		return Result{Task: t, Status: Failed, Reason: ResetFailed, Log: out}
	}
	return Sync(ctx, t)
}

func clone(ctx context.Context, t Task) Result {
	fail := func(reason Reason, log string) Result {
		return Result{Task: t, Status: Failed, Reason: reason, Log: log}
	}
	// A file in the way is checked for first: on Windows, reading it as a directory reports that
	// it doesn't exist.
	if info, err := os.Stat(t.Dir); err == nil && !info.IsDir() {
		return fail(NotARepo, "")
	}
	// A file further up the path is left for MkdirAll to report, as it is on Windows, where
	// reading through a file reports that the path doesn't exist.
	if entries, err := os.ReadDir(t.Dir); err == nil && len(entries) > 0 {
		return fail(NotARepo, "")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return fail(NotARepo, err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(t.Dir), 0o755); err != nil {
		return fail(CloneFailed, err.Error())
	}
	if out, err := git(ctx, "", "clone", "--quiet", t.URL, t.Dir); err != nil {
		return fail(CloneFailed, out)
	}
	r := Result{Task: t, Status: Cloned}
	if !hasRef(ctx, t.Dir, "HEAD") {
		r.Note = "empty"
	}
	return r
}

func update(ctx context.Context, t Task) Result {
	dir := t.Dir
	fail := func(reason Reason, log string) Result {
		return Result{Task: t, Status: Failed, Reason: reason, Log: log}
	}

	if t.OtherURL != "" {
		if origin, err := git(ctx, dir, "remote", "get-url", "origin"); err == nil && sameRemote(origin, t.OtherURL) {
			_, _ = git(ctx, dir, "remote", "set-url", "origin", t.URL)
		}
	}
	status, _ := git(ctx, dir, "status", "--porcelain", "--untracked-files=no")
	dirty := status != ""
	blame := func(otherwise Reason) Reason {
		if dirty {
			return Dirty
		}
		return otherwise
	}

	if out, err := git(ctx, dir, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return fail(FetchFailed, out)
	}
	upstream := "refs/remotes/origin/" + t.Branch
	if t.Branch == "" || !hasRef(ctx, dir, upstream) {
		return Result{Task: t, Status: Current, Note: "empty"}
	}
	if head, _ := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); head != t.Branch {
		if out, err := git(ctx, dir, "checkout", "--quiet", t.Branch); err != nil {
			return fail(blame(CheckoutFailed), out)
		}
	}
	before, _ := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if out, err := git(ctx, dir, "merge", "--ff-only", "--quiet", upstream); err != nil {
		return fail(blame(Diverged), out)
	}
	after, _ := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if before == after {
		return Result{Task: t, Status: Current}
	}

	span := after
	if before != "" {
		span = before + ".." + after
	}
	r := Result{Task: t, Status: Updated}
	if n, err := git(ctx, dir, "rev-list", "--count", span); err == nil {
		r.Note = n + " new commit"
		if n != "1" {
			r.Note += "s"
		}
	}
	return r
}

func hasRef(ctx context.Context, dir, ref string) bool {
	_, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// git runs git in dir and returns its trimmed combined output. It never prompts for credentials,
// so a repository that needs them fails instead of hanging a worker.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// GIT_TERMINAL_PROMPT=0 alone still lets git run an askpass program (GIT_ASKPASS, core.askPass,
	// SSH_ASKPASS); an empty GIT_ASKPASS overrides all three. Credential helpers such as Git
	// Credential Manager still run, so a stored sign-in keeps working.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return "interrupted", fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	return strings.TrimSpace(string(out)), err
}

// sameRemote reports whether two remote URLs name the same repository, ignoring case, user
// info ("org@dev.azure.com"), a trailing slash and a ".git" suffix.
func sameRemote(a, b string) bool {
	return normalizeRemote(a) == normalizeRemote(b)
}

func normalizeRemote(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		s = u.Scheme + "://" + u.Host + u.EscapedPath()
	}
	s = strings.TrimSuffix(s, "/")
	return strings.TrimSuffix(s, ".git")
}
