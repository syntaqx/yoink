package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTargetsFromURLs(t *testing.T) {
	reg := registry()
	for arg, want := range map[string][2]string{
		"https://github.com/acme":                {"github", "acme"},
		"github.com/acme/":                       {"github", "acme"},
		"https://www.github.com/acme":            {"github", "acme"},
		"https://dev.azure.com/contoso":          {"ado", "contoso"},
		"dev.azure.com/contoso/My%20Project":     {"ado", "contoso/My Project"},
		"https://dev.azure.com/contoso/Website/": {"ado", "contoso/Website"},
	} {
		got, err := targetsFromURLs(reg, []string{arg})
		if err != nil {
			t.Errorf("%s: %v", arg, err)
			continue
		}
		if name := got[0].provider.Info().Name; name != want[0] || got[0].name != want[1] {
			t.Errorf("%s: got (%s, %q), want (%s, %q)", arg, name, got[0].name, want[0], want[1])
		}
	}
	for _, bad := range []string{"acme", "https://gitlab.example/acme", "https://github.com/"} {
		if _, err := targetsFromURLs(reg, []string{bad}); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func TestTargetsFor(t *testing.T) {
	gh := registry().Lookup("gh")
	got, err := targetsFor(gh, []string{" acme/ ", "https://github.com/acme-labs"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].name != "acme" || got[1].name != "acme-labs" {
		t.Errorf("got %q, %q", got[0].name, got[1].name)
	}
	if _, err := targetsFor(gh, []string{"https://dev.azure.com/contoso"}); err == nil {
		t.Error("want an error for another provider's URL")
	}
	if _, err := targetsFor(gh, []string{"/"}); err == nil {
		t.Error("want an error for an empty target")
	}
}

func TestLookupAliases(t *testing.T) {
	reg := registry()
	for alias, want := range map[string]string{"gh": "github", "GitHub": "github", "azure-devops": "ado", "ADO": "ado"} {
		if p := reg.Lookup(alias); p == nil || p.Info().Name != want {
			t.Errorf("Lookup(%q) = %v, want %s", alias, p, want)
		}
	}
}

func TestResolveRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	abs := filepath.Join(t.TempDir(), "src")
	for flag, want := range map[string]string{
		"":         filepath.Join(home, "Code"),
		"~":        home,
		"~/src":    filepath.Join(home, "src"),
		abs:        abs,
		"relative": mustAbs(t, "relative"),
	} {
		got, err := resolveRoot(flag)
		if err != nil || got != want {
			t.Errorf("resolveRoot(%q) = %q, %v; want %q", flag, got, err, want)
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
