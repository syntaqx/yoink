package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatch(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".yoinkignore")
	content := "# legacy stuff\n\nacme/legacy-*\n  */sandbox  \ncontoso/OldProject/\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load([]string{file}, []string{`acme\scratch`})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		"acme/legacy-api":           true,
		"ACME/Legacy-Web":           true,
		"acme/api":                  false,
		"other/sandbox":             true,
		"acme/scratch":              true,
		"contoso/oldproject/web":    true, // everything inside an ignored directory
		"contoso/NewProject/web":    false,
		"acme/legacy-api-not-quite": true,
	} {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestMissingFileIsFine(t *testing.T) {
	m, err := Load([]string{filepath.Join(t.TempDir(), "nope")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Match("acme/api") {
		t.Error("empty matcher matched")
	}
}

func TestSourcesAddUp(t *testing.T) {
	dir := t.TempDir()
	global, local := filepath.Join(dir, "global"), filepath.Join(dir, "local")
	if err := os.WriteFile(global, []byte("*/legacy-*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("acme/sandbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load([]string{global, local, filepath.Join(dir, "missing")}, []string{"acme/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"other/legacy-api", "acme/sandbox", "acme/tmp"} {
		if !m.Match(p) {
			t.Errorf("Match(%q) = false, want true", p)
		}
	}
	if m.Match("acme/api") {
		t.Error("Match(acme/api) = true, want false")
	}
}

func TestBadPattern(t *testing.T) {
	if _, err := Load(nil, []string{"acme/[oops"}); err == nil {
		t.Error("want an error for a malformed pattern")
	}
}
