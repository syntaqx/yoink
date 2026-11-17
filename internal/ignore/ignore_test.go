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
	m, err := Load(file, []string{`acme\scratch`})
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
	m, err := Load(filepath.Join(t.TempDir(), "nope"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Match("acme/api") {
		t.Error("empty matcher matched")
	}
}

func TestBadPattern(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope"), []string{"acme/[oops"}); err == nil {
		t.Error("want an error for a malformed pattern")
	}
}
