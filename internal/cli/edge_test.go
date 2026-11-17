package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/syntaqx/yoink/internal/provider"
)

func TestModuleVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		bi   *debug.BuildInfo
		ok   bool
		want string
	}{
		{"go install module@version", &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}}, true, "v1.4.0"},
		{"pseudo-version", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261117190000-6e30d38abcde"}}, true, "v0.0.0-20261117190000-6e30d38abcde"},
		{"local build", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "dev"},
		{"no version", &debug.BuildInfo{}, true, "dev"},
		{"no build info", nil, false, "dev"},
	} {
		if got := moduleVersion(tc.bi, tc.ok); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSplitURL(t *testing.T) {
	for _, tc := range []struct {
		in         string
		host, path string
		ok         bool
	}{
		{"https://github.com/acme", "github.com", "/acme", true},
		{"github.com/acme", "github.com", "/acme", true},
		{"  https://GitHub.com:443/acme  ", "GitHub.com", "/acme", true},
		{"dev.azure.com/contoso/My%20Project", "dev.azure.com", "/contoso/My Project", true},
		// Unescaped exactly once: %25 is a literal percent sign, not the start of another escape.
		{"dev.azure.com/contoso/100%25", "dev.azure.com", "/contoso/100%", true},
		{"dev.azure.com/contoso/%2541", "dev.azure.com", "/contoso/%41", true},
		{"acme", "", "", false},                  // a bare name
		{"acme/repo", "", "", false},             // no dot before the first slash
		{"https://", "", "", false},              // no host
		{"https://a.example/%zz", "", "", false}, // a broken escape
	} {
		host, path, ok := splitURL(tc.in)
		if host != tc.host || path != tc.path || ok != tc.ok {
			t.Errorf("splitURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, host, path, ok, tc.host, tc.path, tc.ok)
		}
	}
}

// noHome makes os.UserHomeDir fail on every OS.
func noHome(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HOME", "USERPROFILE", "home", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("this OS finds a home directory without the environment")
	}
}

func TestWithoutAHomeDirectory(t *testing.T) {
	noHome(t)
	if got := globalIgnoreFile(); got != "" {
		t.Errorf("globalIgnoreFile() = %q, want none", got)
	}
	for _, flag := range []string{"", "~", "~/src"} {
		if _, err := resolveRoot(flag); err == nil || !strings.Contains(err.Error(), "finding home directory") {
			t.Errorf("resolveRoot(%q): err = %v", flag, err)
		}
	}
	// An explicit root doesn't need one.
	abs := t.TempDir()
	if got, err := resolveRoot(abs); err != nil || got != abs {
		t.Errorf("resolveRoot(%q) = %q, %v", abs, got, err)
	}
}

func TestEndToEndWithoutAHomeDirectory(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	noHome(t)

	if r := s.yoink("fake", "acme"); r.code != 2 || !strings.Contains(r.stderr, "yoink: finding home directory") {
		t.Errorf("default root: code %d, stderr %q", r.code, r.stderr)
	}
	// With an explicit root there's nothing to find; the global ignore file is simply skipped.
	s.yoink("fake", "acme", "--root", t.TempDir()).expect(t, 0, "+ acme/app cloned")
}

func TestEndToEndValidFlags(t *testing.T) {
	s := newServer(t)
	s.add("acme", "app")
	s.add("acme", "lib")

	// The fake serves HTTPS URLs only, so asking for SSH falls back to HTTPS rather than failing.
	s.yoink("fake", "acme", "--root", t.TempDir(), "-j", "2", "--protocol", "SSH").expect(t, 0, "+ 2 cloned")

	t.Setenv("YOINK_JOBS", "1")
	t.Setenv("YOINK_PROTOCOL", "https")
	s.yoink("fake", "acme", "--root", t.TempDir()).expect(t, 0, "+ 2 cloned")
}

func TestDiscoverAnimatesOnATerminal(t *testing.T) {
	s, out := testSession(t)
	s.live = true
	fp := newFake(provider.SSH)
	fp.repos["acme"] = []provider.Repo{repo("acme/app"), repo("acme/lib")}

	s.discover(context.Background(), []target{{fp, "acme"}})

	// The spinner draws at least its first frame, erases itself, and then the result line follows.
	got := out.String()
	if !strings.HasPrefix(got, "\r\x1b[K  ⠋ Fake acme discovering… ") {
		t.Errorf("no spinner frame: %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[K  ✓ Fake acme  2 repositories · 2 to sync\n") {
		t.Errorf("the spinner wasn't erased before the result: %q", got)
	}
}

// Run is the real entry point: it wires the process's own stdin, stdout and stderr.
func TestRunUsesTheProcessStreams(t *testing.T) {
	isolate(t)
	stdout, stderr := capture(t, &os.Stdout), capture(t, &os.Stderr)

	old := Version
	Version = "v7.7.7"
	t.Cleanup(func() { Version = old })
	if code := Run([]string{"--version"}); code != 0 {
		t.Errorf("--version: exit code %d", code)
	}
	if code := Run([]string{"not-a-provider"}); code != 2 {
		t.Errorf("unknown provider: exit code %d", code)
	}

	if got := stdout(); !strings.Contains(got, "v7.7.7") {
		t.Errorf("stdout = %q", got)
	}
	if got := stderr(); !strings.Contains(got, `yoink: unknown provider "not-a-provider"`) {
		t.Errorf("stderr = %q", got)
	}
}

// capture points *f at a temporary file until the test ends; read returns what was written.
func capture(t *testing.T, f **os.File) (read func() string) {
	t.Helper()
	tmp, err := os.Create(filepath.Join(t.TempDir(), "stream"))
	if err != nil {
		t.Fatal(err)
	}
	old := *f
	*f = tmp
	t.Cleanup(func() { *f = old; tmp.Close() })
	return func() string {
		b, err := os.ReadFile(tmp.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}
