package gitcred

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// isolate points git at an empty global config and no system config, so the developer's own
// credential helpers never answer. It returns the config file for the test to add to.
func isolate(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return cfg
}

func gitConfig(t *testing.T, cfg string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"config", "--file", cfg}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git config %v: %v\n%s", args, err, out)
	}
}

// storeHelper installs a credential helper that answers every lookup with password, and records
// what git asked it in the returned file.
func storeHelper(t *testing.T, password string) (requests string) {
	t.Helper()
	cfg := isolate(t)
	requests = filepath.Join(t.TempDir(), "requests")
	helper := `!f() { test "$1" = get || exit 0; cat >> '` + filepath.ToSlash(requests) + `'; ` +
		`echo username=u; echo password=` + password + `; }; f`
	gitConfig(t, cfg, "credential.helper", helper)
	gitConfig(t, cfg, "credential.useHttpPath", "true")
	return requests
}

func TestFillReturnsTheStoredPassword(t *testing.T) {
	requests := storeHelper(t, "s3cret")
	if got := Fill(context.Background(), "dev.azure.com", "contoso"); got != "s3cret" {
		t.Fatalf("Fill = %q, want s3cret", got)
	}
	b, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	if want := "protocol=https\nhost=dev.azure.com\npath=contoso\n"; string(b) != want {
		t.Errorf("helper was asked %q, want %q", b, want)
	}
}

func TestFillWithoutPath(t *testing.T) {
	requests := storeHelper(t, "tok")
	if got := Fill(context.Background(), "github.com", ""); got != "tok" {
		t.Fatalf("Fill = %q, want tok", got)
	}
	if b, _ := os.ReadFile(requests); string(b) != "protocol=https\nhost=github.com\n" {
		t.Errorf("helper was asked %q", b)
	}
}

func TestFillNeverPrompts(t *testing.T) {
	cfg := isolate(t)

	// Outside a terminal, git prompts through an askpass program (VS Code's terminal sets
	// GIT_ASKPASS). With nothing stored, Fill must come back empty without running any of them.
	dir := t.TempDir()
	marker := filepath.Join(dir, "asked")
	askpass := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\necho asked >> '" + filepath.ToSlash(marker) + "'\necho typed-in\n"
	if err := os.WriteFile(askpass, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS", askpass)
	gitConfig(t, cfg, "core.askPass", askpass)

	if got := Fill(context.Background(), "example.invalid", ""); got != "" {
		t.Errorf("Fill = %q, want nothing", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("Fill ran an askpass program")
	}
}

func TestFillWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := Fill(context.Background(), "github.com", ""); got != "" {
		t.Errorf("Fill = %q, want nothing", got)
	}
}
