package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/syntaqx/yoink/internal/ignore"
	"github.com/syntaqx/yoink/internal/mirror"
	"github.com/syntaqx/yoink/internal/provider"
)

// fakeProvider serves canned repositories (or errors) per target.
type fakeProvider struct {
	info  provider.Info
	repos map[string][]provider.Repo
	errs  map[string]error
}

func newFake(protocol provider.Protocol) *fakeProvider {
	return &fakeProvider{
		info: provider.Info{
			Name: "fake", Aliases: []string{"fk"}, Title: "Fake", Hosts: []string{"git.example"},
			Usage: "<owner>", Auth: "none", Protocol: protocol,
		},
		repos: map[string][]provider.Repo{},
		errs:  map[string]error{},
	}
}

func (f *fakeProvider) Info() provider.Info { return f.info }

func (f *fakeProvider) Discover(_ context.Context, target string, progress func(int)) ([]provider.Repo, error) {
	if err := f.errs[target]; err != nil {
		return nil, err
	}
	progress(len(f.repos[target]))
	return f.repos[target], nil
}

func repo(path string) provider.Repo {
	return provider.Repo{
		Path:          path,
		SSHURL:        "git@git.example:" + path + ".git",
		HTTPSURL:      "https://git.example/" + path + ".git",
		DefaultBranch: "main",
	}
}

// testSession returns a session writing uncolored output to the returned buffer.
func testSession(t *testing.T, patterns ...string) (*session, *bytes.Buffer) {
	t.Helper()
	m, err := ignore.Load(nil, patterns)
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &session{
		out:    out,
		in:     strings.NewReader(""),
		root:   t.TempDir(),
		jobs:   1, // results arrive in task order
		ignore: m,
		syncRepo: func(context.Context, mirror.Task) mirror.Result {
			t.Error("syncRepo called unexpectedly")
			return mirror.Result{}
		},
		resetRepo: func(context.Context, mirror.Task) mirror.Result {
			t.Error("resetRepo called unexpectedly")
			return mirror.Result{}
		},
	}, out
}

// lines splits output into lines with trailing whitespace trimmed, dropping blank ones.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimRight(l, " \r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func assertLines(t *testing.T, got string, want ...string) {
	t.Helper()
	if g := lines(got); !reflect.DeepEqual(g, want) {
		t.Errorf("output:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
}

func TestDiscoverFiltersRepositories(t *testing.T) {
	s, out := testSession(t, "acme/legacy-*")
	fp := newFake(provider.SSH)
	disabled := repo("acme/disabled")
	disabled.Skip = "disabled"
	fp.repos["acme"] = []provider.Repo{
		repo("acme/app"),
		repo("acme/legacy-api"), // ignored by pattern
		disabled,                // skipped by the provider
		repo("acme/../../escape"),
		repo("noslash"),
		repo("ACME/App"), // a case-insensitive duplicate of acme/app
	}

	tasks, skipped, ok := s.discover(context.Background(), []target{{fp, "acme"}})
	if !ok {
		t.Fatal("ok = false")
	}
	want := []mirror.Task{{
		Name:     "acme/app",
		Dir:      filepath.Join(s.root, "acme", "app"),
		URL:      "git@git.example:acme/app.git",
		OtherURL: "https://git.example/acme/app.git",
		Branch:   "main",
	}}
	if !reflect.DeepEqual(tasks, want) {
		t.Errorf("tasks = %+v\nwant %+v", tasks, want)
	}
	if want := map[string]int{"disabled": 1, "ignored": 1, "unsafe path": 2}; !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
	// The duplicate is neither synced nor reported as skipped.
	assertLines(t, out.String(), "  ✓ Fake acme  6 repositories · 1 to sync · 1 disabled · 1 ignored · 2 unsafe path")
}

func TestDiscoverDeduplicatesAcrossTargets(t *testing.T) {
	s, out := testSession(t)
	fp := newFake(provider.SSH)
	fp.repos["acme"] = []provider.Repo{repo("acme/app"), repo("acme/lib")}
	fp.repos["https://git.example/acme"] = []provider.Repo{repo("Acme/App")}

	tasks, _, ok := s.discover(context.Background(), []target{{fp, "acme"}, {fp, "https://git.example/acme"}})
	if !ok || len(tasks) != 2 {
		t.Fatalf("ok = %v, tasks = %+v", ok, tasks)
	}
	assertLines(t, out.String(),
		"  ✓ Fake acme  2 repositories · 2 to sync",
		"  ✓ Fake https://git.example/acme  1 repository · 0 to sync",
	)
}

func TestDiscoverProtocol(t *testing.T) {
	for _, tc := range []struct {
		name             string
		def, flag        provider.Protocol
		wantURL, wantAlt string
	}{
		{"provider default ssh", provider.SSH, "", "git@git.example:a/b.git", "https://git.example/a/b.git"},
		{"provider default https", provider.HTTPS, "", "https://git.example/a/b.git", "git@git.example:a/b.git"},
		{"flag overrides default", provider.SSH, provider.HTTPS, "https://git.example/a/b.git", "git@git.example:a/b.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testSession(t)
			s.protocol = tc.flag
			fp := newFake(tc.def)
			fp.repos["a"] = []provider.Repo{repo("a/b")}
			tasks, _, _ := s.discover(context.Background(), []target{{fp, "a"}})
			if len(tasks) != 1 || tasks[0].URL != tc.wantURL || tasks[0].OtherURL != tc.wantAlt {
				t.Errorf("tasks = %+v, want URL %s and OtherURL %s", tasks, tc.wantURL, tc.wantAlt)
			}
		})
	}
}

func TestDiscoverKeepsGoingAfterAFailure(t *testing.T) {
	s, out := testSession(t)
	fp := newFake(provider.SSH)
	fp.errs["broken"] = errors.New("HTTP 404: Not Found")
	fp.repos["acme"] = []provider.Repo{repo("acme/app")}

	tasks, _, ok := s.discover(context.Background(), []target{{fp, "broken"}, {fp, "acme"}})
	if ok {
		t.Error("ok = true after a failed target")
	}
	if len(tasks) != 1 || tasks[0].Name != "acme/app" {
		t.Errorf("tasks = %+v, want the healthy target's repository", tasks)
	}
	assertLines(t, out.String(),
		"  ✗ Fake broken",
		"    HTTP 404: Not Found",
		"  ✓ Fake acme  1 repository · 1 to sync",
	)
}

func TestPlan(t *testing.T) {
	s, out := testSession(t)
	existing := filepath.Join(s.root, "acme", "have")
	if err := os.MkdirAll(filepath.Join(existing, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.plan([]mirror.Task{
		{Name: "acme/have", Dir: existing, URL: "u1"},
		{Name: "acme/new", Dir: filepath.Join(s.root, "acme", "new"), URL: "git@git.example:acme/new.git"},
	})
	assertLines(t, out.String(),
		"  ✓ acme/have",
		"  + acme/new git@git.example:acme/new.git",
		"  Dry run: 1 to clone, 1 to sync. Nothing was changed.",
	)
	if _, err := os.Stat(filepath.Join(s.root, "acme", "new")); !errors.Is(err, os.ErrNotExist) {
		t.Error("plan created the clone directory")
	}
}

// scripted returns work that answers each task by name from results.
func scripted(t *testing.T, results map[string]mirror.Result) (mirror.Work, func() []string) {
	var mu sync.Mutex
	var calls []string
	work := func(_ context.Context, task mirror.Task) mirror.Result {
		mu.Lock()
		calls = append(calls, task.Name)
		mu.Unlock()
		r, ok := results[task.Name]
		if !ok {
			t.Errorf("unexpected task %s", task.Name)
		}
		r.Task = task
		return r
	}
	return work, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), calls...) }
}

func tasksNamed(names ...string) []mirror.Task {
	var out []mirror.Task
	for _, n := range names {
		out = append(out, mirror.Task{Name: n})
	}
	return out
}

func TestSyncReportsEachResult(t *testing.T) {
	results := map[string]mirror.Result{
		"a/current": {Status: mirror.Current},
		"a/updated": {Status: mirror.Updated, Note: "2 new commits"},
		"a/cloned":  {Status: mirror.Cloned},
		"a/empty":   {Status: mirror.Cloned, Note: "empty"},
		"a/dirty":   {Status: mirror.Failed, Reason: mirror.Dirty},
		"a/fetch":   {Status: mirror.Failed, Reason: mirror.FetchFailed, Log: "fatal: no route\nhint: check VPN"},
	}
	order := []string{"a/current", "a/updated", "a/cloned", "a/empty", "a/dirty", "a/fetch"}

	t.Run("quiet", func(t *testing.T) {
		s, out := testSession(t)
		work, _ := scripted(t, results)
		got := s.sync(context.Background(), tasksNamed(order...), work)
		if len(got) != len(order) {
			t.Fatalf("got %d results, want %d", len(got), len(order))
		}
		assertLines(t, out.String(),
			"  [1/6] ✓ a/current",
			"  [2/6] ✓ a/updated 2 new commits",
			"  [3/6] + a/cloned cloned",
			"  [4/6] + a/empty cloned (empty)",
			"  [5/6] ✗ a/dirty local changes",
			"  [6/6] ✗ a/fetch fetch failed",
		)
	})

	t.Run("verbose shows git's output", func(t *testing.T) {
		s, out := testSession(t)
		s.verbose = true
		work, _ := scripted(t, results)
		s.sync(context.Background(), tasksNamed("a/fetch", "a/current"), work)
		assertLines(t, out.String(),
			"  [1/2] ✗ a/fetch fetch failed",
			"        fatal: no route",
			"        hint: check VPN",
			"  [2/2] ✓ a/current",
		)
	})
}

func TestSyncPadsTheCounter(t *testing.T) {
	s, out := testSession(t)
	var names []string
	results := map[string]mirror.Result{}
	for i := range 10 {
		n := "a/r" + string(rune('a'+i))
		names = append(names, n)
		results[n] = mirror.Result{Status: mirror.Current}
	}
	work, _ := scripted(t, results)
	s.sync(context.Background(), tasksNamed(names...), work)
	got := lines(out.String())
	if got[0] != "  [ 1/10] ✓ a/ra" || got[9] != "  [10/10] ✓ a/rj" {
		t.Errorf("counter not aligned:\n%s", strings.Join(got, "\n"))
	}
}

func TestSummarize(t *testing.T) {
	results := []mirror.Result{
		{Task: mirror.Task{Name: "a/current"}, Status: mirror.Current},
		{Task: mirror.Task{Name: "a/updated"}, Status: mirror.Updated},
		{Task: mirror.Task{Name: "a/cloned"}, Status: mirror.Cloned},
		{Task: mirror.Task{Name: "a/dirty"}, Status: mirror.Failed, Reason: mirror.Dirty},
		{Task: mirror.Task{Name: "a/diverged"}, Status: mirror.Failed, Reason: mirror.Diverged},
	}
	rule := "  " + strings.Repeat("─", 44)

	t.Run("everything", func(t *testing.T) {
		s, out := testSession(t)
		failures := s.summarize(results, map[string]int{"ignored": 2, "disabled": 1})
		assertLines(t, out.String(),
			rule,
			"  ✓ 2 synced (1 updated)   + 1 cloned   ✗ 2 failed   ⊘ 1 disabled   ⊘ 2 ignored",
			"  Failed (fix by hand; rerun with -v for git's output)",
			"    ✗ a/diverged diverged",
			"  Local changes (left untouched)",
			"    ✗ a/dirty",
		)
		var names []string
		for _, f := range failures {
			names = append(names, f.Task.Name)
		}
		if want := []string{"a/diverged", "a/dirty"}; !reflect.DeepEqual(names, want) {
			t.Errorf("failures = %v, want %v", names, want)
		}
	})

	t.Run("verbose drops the -v hint", func(t *testing.T) {
		s, out := testSession(t)
		s.verbose = true
		s.summarize(results[4:], nil)
		if !strings.Contains(out.String(), "Failed (fix by hand)\n") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("all current", func(t *testing.T) {
		s, out := testSession(t)
		if failures := s.summarize(results[:1], nil); len(failures) != 0 {
			t.Errorf("failures = %v", failures)
		}
		assertLines(t, out.String(), rule, "  ✓ 1 synced")
	})
}

func TestOfferReset(t *testing.T) {
	failures := []mirror.Result{
		{Task: mirror.Task{Name: "a/diverged"}, Status: mirror.Failed, Reason: mirror.Diverged},
		{Task: mirror.Task{Name: "a/dirty"}, Status: mirror.Failed, Reason: mirror.Dirty},
		{Task: mirror.Task{Name: "a/stuck"}, Status: mirror.Failed, Reason: mirror.Dirty},
	}
	prompt := "Discard local changes in these 2 repo(s) with git reset --hard and retry? [y/N]"

	for _, tc := range []struct {
		name        string
		interactive bool
		cancelled   bool
		answer      string
		wantPrompt  bool
		wantReset   []string
		wantLeft    []string
	}{
		{"not a terminal", false, false, "y\n", false, nil, []string{"a/diverged", "a/dirty", "a/stuck"}},
		{"interrupted", true, true, "y\n", false, nil, []string{"a/diverged", "a/dirty", "a/stuck"}},
		{"declined", true, false, "n\n", true, nil, []string{"a/diverged", "a/dirty", "a/stuck"}},
		{"default is no", true, false, "\n", true, nil, []string{"a/diverged", "a/dirty", "a/stuck"}},
		{"no input", true, false, "", true, nil, []string{"a/diverged", "a/dirty", "a/stuck"}},
		{"accepted", true, false, "y\n", true, []string{"a/dirty", "a/stuck"}, []string{"a/diverged", "a/stuck"}},
		{"accepted loudly", true, false, "  YES \r\n", true, []string{"a/dirty", "a/stuck"}, []string{"a/diverged", "a/stuck"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, out := testSession(t)
			s.interactive = tc.interactive
			s.in = strings.NewReader(tc.answer)
			reset, calls := scripted(t, map[string]mirror.Result{
				"a/dirty": {Status: mirror.Updated, Note: "1 new commit"},
				"a/stuck": {Status: mirror.Failed, Reason: mirror.ResetFailed},
			})
			s.resetRepo = reset
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}

			left := s.offerReset(ctx, failures)

			if got := strings.Contains(out.String(), prompt); got != tc.wantPrompt {
				t.Errorf("prompted = %v, want %v; output:\n%s", got, tc.wantPrompt, out)
			}
			if got := calls(); !reflect.DeepEqual(got, tc.wantReset) {
				t.Errorf("reset %v, want %v", got, tc.wantReset)
			}
			var names []string
			for _, r := range left {
				names = append(names, r.Task.Name)
			}
			if !reflect.DeepEqual(names, tc.wantLeft) {
				t.Errorf("remaining = %v, want %v", names, tc.wantLeft)
			}
		})
	}
}

func TestOfferResetWithoutDirtyRepos(t *testing.T) {
	s, out := testSession(t)
	s.interactive = true
	failures := []mirror.Result{{Task: mirror.Task{Name: "a/x"}, Status: mirror.Failed, Reason: mirror.FetchFailed}}
	if left := s.offerReset(context.Background(), failures); len(left) != 1 {
		t.Errorf("remaining = %v", left)
	}
	if out.Len() != 0 {
		t.Errorf("asked with nothing to reset:\n%s", out)
	}
}

func TestRun(t *testing.T) {
	ok := func(context.Context, mirror.Task) mirror.Result { return mirror.Result{Status: mirror.Current} }
	fail := func(context.Context, mirror.Task) mirror.Result {
		return mirror.Result{Status: mirror.Failed, Reason: mirror.Diverged}
	}

	for _, tc := range []struct {
		name      string
		repos     []provider.Repo
		discovery error
		dryRun    bool
		cancelled bool
		work      mirror.Work
		wantOK    bool
		want      string // a line the output must contain
		notWant   string
	}{
		{name: "nothing found", wantOK: true, want: "Nothing to sync."},
		{name: "discovery failed", discovery: errors.New("boom"), wantOK: false, want: "✗ Fake acme", notWant: "Nothing to sync."},
		{name: "all synced", repos: []provider.Repo{repo("acme/a")}, work: ok, wantOK: true, want: "✓ All repos up to date."},
		{name: "a repo failed", repos: []provider.Repo{repo("acme/a")}, work: fail, wantOK: false, want: "✗ 1 failed", notWant: "All repos up to date"},
		{name: "dry run", repos: []provider.Repo{repo("acme/a")}, dryRun: true, wantOK: true, want: "Dry run: 1 to clone, 0 to sync.", notWant: "All repos up to date"},
		{name: "interrupted", repos: []provider.Repo{repo("acme/a")}, cancelled: true, work: ok, wantOK: false, want: "Interrupted.", notWant: "All repos up to date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, out := testSession(t)
			s.dryRun = tc.dryRun
			if tc.work != nil {
				s.syncRepo = tc.work
			}
			fp := newFake(provider.SSH)
			fp.repos["acme"] = tc.repos
			if tc.discovery != nil {
				fp.errs["acme"] = tc.discovery
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}

			if got := s.run(ctx, []target{{fp, "acme"}}); got != tc.wantOK {
				t.Errorf("run = %v, want %v", got, tc.wantOK)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(out.String(), tc.notWant) {
				t.Errorf("output has %q:\n%s", tc.notWant, out)
			}
			if !strings.Contains(out.String(), "→ "+s.root) {
				t.Errorf("output doesn't name the root:\n%s", out)
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	if got := paren(""); got != "" {
		t.Errorf("paren(\"\") = %q", got)
	}
	if got := paren("x"); got != "(x)" {
		t.Errorf("paren(x) = %q", got)
	}
	for n, want := range map[int]string{0: "0 repos", 1: "1 repo", 2: "2 repos"} {
		if got := plural(n, "repo", "repos"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
	if got := sortedKeys(map[string]int{"b": 1, "a": 2, "c": 0}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("sortedKeys = %v", got)
	}
}
