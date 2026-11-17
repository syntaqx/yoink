package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syntaqx/yoink/internal/provider"
)

// These tests drive the whole command, flags to exit code, against real git repositories on disk.

// isolate keeps the developer's environment out of a test: git config, ignore files, colors and
// the YOINK_* settings.
func isolate(t *testing.T) {
	t.Helper()
	for _, k := range []string{"YOINK_ROOT", "YOINK_JOBS", "YOINK_PROTOCOL", "NO_COLOR", "FORCE_COLOR"} {
		t.Setenv(k, "")
		os.Unsetenv(k) // flag sources treat a set-but-empty variable as set
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// server hosts bare repositories and serves them through a fake provider.
type server struct {
	t    *testing.T
	dir  string
	fake *fakeProvider
}

func newServer(t *testing.T) *server {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolate(t)
	return &server{t: t, dir: t.TempDir(), fake: newFake(provider.HTTPS)}
}

// add creates owner/name with one commit on main and lists it under owner.
func (s *server) add(owner, name string) {
	s.t.Helper()
	bare := filepath.Join(s.dir, owner, name+".git")
	gitIn(s.t, s.dir, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	s.push(owner, name, "README", "hello")
	s.fake.repos[owner] = append(s.fake.repos[owner], provider.Repo{
		Path: owner + "/" + name, HTTPSURL: bare, DefaultBranch: "main",
	})
}

// push commits file=content to owner/name's main branch.
func (s *server) push(owner, name, file, content string) {
	s.t.Helper()
	bare := filepath.Join(s.dir, owner, name+".git")
	work := s.t.TempDir()
	gitIn(s.t, work, "clone", "--quiet", bare, ".")
	if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
	gitIn(s.t, work, "add", file)
	gitIn(s.t, work, "commit", "--quiet", "-m", "change "+file)
	gitIn(s.t, work, "push", "--quiet", "origin", "HEAD:main")
}

type result struct {
	code           int
	stdout, stderr string
}

func (s *server) yoink(args ...string) result {
	return s.yoinkCtx(context.Background(), args...)
}

func (s *server) yoinkCtx(ctx context.Context, args ...string) result {
	s.t.Helper()
	var stdout, stderr bytes.Buffer
	code := runWith(ctx, provider.Registry{s.fake}, args, strings.NewReader(""), &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

func (r result) expect(t *testing.T, code int, stdout ...string) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	for _, want := range stdout {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestEndToEndCloneThenUpdate(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	s.add("acme", "lib")
	root := t.TempDir()

	s.yoink("fake", "acme", "--root", root).expect(t, 0,
		"✓ Fake acme  2 repositories · 2 to sync",
		"+ acme/app cloned",
		"+ acme/lib cloned",
		"+ 2 cloned",
		"✓ All repos up to date.",
	)
	if b, err := os.ReadFile(filepath.Join(root, "acme", "app", "README")); err != nil || string(b) != "hello" {
		t.Fatalf("clone content = %q, %v", b, err)
	}

	s.push("acme", "app", "CHANGELOG", "v2")
	s.yoink("fake", "acme", "--root", root).expect(t, 0,
		"✓ acme/app 1 new commit",
		"✓ 2 synced (1 updated)",
		"✓ All repos up to date.",
	)
	if _, err := os.Stat(filepath.Join(root, "acme", "app", "CHANGELOG")); err != nil {
		t.Errorf("update not fast-forwarded: %v", err)
	}

	r := s.yoink("fake", "acme", "--root", root)
	r.expect(t, 0, "✓ 2 synced\n", "✓ All repos up to date.")
	if strings.Contains(r.stdout, "updated") {
		t.Errorf("a no-op run reported updates:\n%s", r.stdout)
	}
}

func TestEndToEndDryRunChangesNothing(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	root := t.TempDir()

	for _, flag := range []string{"--dry-run", "-n"} {
		s.yoink("fake", "acme", "--root", root, flag).expect(t, 0, "+ acme/app", "Dry run: 1 to clone, 0 to sync. Nothing was changed.")
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Fatalf("%s wrote to the root: %v", flag, entries)
		}
	}
}

func TestEndToEndLocalChangesAreLeftAlone(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	root := t.TempDir()
	s.yoink("fake", "acme", "--root", root).expect(t, 0)

	// A local edit to the same file the server changes can't be fast-forwarded over.
	readme := filepath.Join(root, "acme", "app", "README")
	if err := os.WriteFile(readme, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.push("acme", "app", "README", "theirs")

	// stdin isn't a terminal, so yoink reports instead of asking.
	r := s.yoink("fake", "acme", "--root", root)
	r.expect(t, 1, "✗ acme/app local changes", "Local changes (left untouched)")
	if strings.Contains(r.stdout, "Discard local changes") {
		t.Errorf("prompted without a terminal:\n%s", r.stdout)
	}
	if b, _ := os.ReadFile(readme); string(b) != "mine" {
		t.Errorf("local change lost: README = %q", b)
	}
}

func TestEndToEndIgnoreSources(t *testing.T) {
	s := newServer(t)
	for _, n := range []string{"app", "legacy-a", "sandbox", "scratch"} {
		s.add("acme", n)
	}
	root := t.TempDir()

	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "yoink", "ignore")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("# every root\nacme/sandbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".yoinkignore"), []byte("acme/legacy-*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.yoink("fake", "acme", "--root", root, "-i", "acme/scratch").expect(t, 0,
		"4 repositories · 1 to sync · 3 ignored",
		"⊘ 3 ignored",
	)
	entries, _ := os.ReadDir(filepath.Join(root, "acme"))
	if len(entries) != 1 || entries[0].Name() != "app" {
		t.Errorf("cloned %v, want only app", entries)
	}
}

func TestEndToEndBadIgnorePattern(t *testing.T) {
	s := newServer(t)
	r := s.yoink("fake", "acme", "--root", t.TempDir(), "--ignore", "[")
	if r.code != 2 || !strings.Contains(r.stderr, `bad ignore pattern "["`) {
		t.Errorf("code %d, stderr %q", r.code, r.stderr)
	}
}

func TestEndToEndRootFromEnvironment(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	root := t.TempDir()
	t.Setenv("YOINK_ROOT", root)

	s.yoink("fake", "acme").expect(t, 0, "→ "+root)
	if _, err := os.Stat(filepath.Join(root, "acme", "app", ".git")); err != nil {
		t.Errorf("not cloned under YOINK_ROOT: %v", err)
	}
}

func TestEndToEndURLTargets(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	s.add("acme-labs", "tool")
	root := t.TempDir()

	// Several owners in one run each get their own directory under the root.
	s.yoink("https://git.example/acme", "git.example/acme-labs/", "--root", root).expect(t, 0,
		"✓ Fake acme  1 repository · 1 to sync",
		"✓ Fake acme-labs  1 repository · 1 to sync",
		"+ 2 cloned",
	)
	for _, dir := range []string{"acme/app", "acme-labs/tool"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), ".git")); err != nil {
			t.Errorf("%s not cloned: %v", dir, err)
		}
	}

	// A provider's alias accepts its URLs too.
	s.yoink("fk", "https://git.example/acme", "--root", root).expect(t, 0, "✓ Fake acme  1 repository · 1 to sync")
}

func TestEndToEndDiscoveryFailure(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	s.fake.errs["ghost"] = &os.PathError{Op: "list", Path: "ghost", Err: os.ErrNotExist}
	root := t.TempDir()

	// The healthy target still syncs, but the run fails.
	r := s.yoink("fake", "ghost", "acme", "--root", root)
	r.expect(t, 1, "✗ Fake ghost", "+ acme/app cloned")
	if strings.Contains(r.stdout, "All repos up to date") {
		t.Errorf("claimed success:\n%s", r.stdout)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want the failure reported on stdout only", r.stderr)
	}
}

func TestEndToEndInterrupted(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.yoinkCtx(ctx, "fake", "acme", "--root", root).expect(t, 1, "Interrupted.")
	if _, err := os.Stat(filepath.Join(root, "acme", "app")); !os.IsNotExist(err) {
		t.Errorf("cloned after interrupt: %v", err)
	}
}

func TestEndToEndUsageErrors(t *testing.T) {
	s := newServer(t)
	for _, tc := range []struct {
		args   []string
		stderr string
	}{
		{[]string{"fake"}, "usage: yoink fake <owner>..."},
		{[]string{"fake", "acme", "--jobs", "0"}, "--jobs must be at least 1, got 0 (see yoink fake --help)"},
		{[]string{"--protocol", "ftp", "fake", "acme"}, `--protocol must be ssh or https, got "ftp" (see yoink --help)`},
		{[]string{"fake", "acme", "--bogus"}, "flag provided but not defined: -bogus (see yoink fake --help)"},
		{[]string{"acme"}, `unknown provider "acme" (try: yoink github acme)`},
		{[]string{"https://gitlab.example/acme"}, `no provider for host "gitlab.example"`},
		{[]string{"https://git.example/"}, "names a host but no owner"},
		{[]string{"fake", "https://other.example/acme"}, "is not a Fake URL"},
		{[]string{"fake", "/"}, `invalid target "/"`},
	} {
		r := s.yoink(tc.args...)
		// One line on stderr: no help dump, no repeated message, nothing on stdout.
		if r.code != 2 || !strings.HasPrefix(r.stderr, "yoink: ") || !strings.HasSuffix(r.stderr, tc.stderr+"\n") ||
			strings.Count(r.stderr, "\n") != 1 || r.stdout != "" {
			t.Errorf("yoink %s: code %d, stdout %q, stderr %q; want 2 and one line ending %q",
				strings.Join(tc.args, " "), r.code, r.stdout, r.stderr, tc.stderr)
		}
	}
}

func TestEndToEndFlagsFromEnvironmentAreValidated(t *testing.T) {
	s := newServer(t)
	t.Setenv("YOINK_JOBS", "0")
	if r := s.yoink("fake", "acme", "--root", t.TempDir()); r.code != 2 || !strings.Contains(r.stderr, "--jobs must be at least 1") {
		t.Errorf("code %d, stderr %q", r.code, r.stderr)
	}
}

func TestEndToEndHelpAndVersion(t *testing.T) {
	s := newServer(t)

	r := s.yoink()
	r.expect(t, 0, "yoink <provider> <target>...", "fake, fk", "--dry-run")

	r = s.yoink("fake", "--help")
	r.expect(t, 0, "sync Fake repositories", "Auth: none", "Default protocol: https")

	old := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = old })
	s.yoink("--version").expect(t, 0, "v9.9.9")
}

func TestEndToEndColor(t *testing.T) {
	s := newServer(t)
	run := func(env map[string]string, args ...string) bool {
		t.Helper()
		for k, v := range env {
			t.Setenv(k, v)
		}
		defer func() {
			for k := range env {
				os.Unsetenv(k)
			}
		}()
		r := s.yoink(append([]string{"fake", "acme", "--root", t.TempDir()}, args...)...)
		return strings.Contains(r.stdout, "\x1b[")
	}

	if run(nil) {
		t.Error("colored output that isn't a terminal")
	}
	if !run(map[string]string{"FORCE_COLOR": "1"}) {
		t.Error("FORCE_COLOR didn't enable color")
	}
	if run(map[string]string{"FORCE_COLOR": "1", "NO_COLOR": "1"}) {
		t.Error("NO_COLOR didn't win over FORCE_COLOR")
	}
	if run(map[string]string{"FORCE_COLOR": "1"}, "--no-color") {
		t.Error("--no-color didn't win over FORCE_COLOR")
	}
}

func TestVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v1.2.3"
	if got := version(); got != "v1.2.3" {
		t.Errorf("version() = %q", got)
	}
	Version = ""
	if got := version(); got == "" {
		t.Error("version() is empty without a build-time version")
	}
}
