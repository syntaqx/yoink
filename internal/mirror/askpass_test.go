package mirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A repository that needs credentials must fail fast, never prompt. Outside a terminal git asks
// through an askpass program (VS Code's terminal sets GIT_ASKPASS), and with several jobs running
// that means a pile of prompts while each worker waits.
func TestCloneNeedingCredentialsNeverPrompts(t *testing.T) {
	newFixture(t) // isolates git from the developer's config, so no credential helper answers

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	marker := filepath.Join(dir, "asked")
	askpass := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\necho asked >> '" + filepath.ToSlash(marker) + "'\necho typed-in\n"
	if err := os.WriteFile(askpass, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS", askpass)

	task := Task{Name: "acme/private", Dir: filepath.Join(dir, "clone"), URL: srv.URL + "/acme/private.git"}
	r := Sync(context.Background(), task)
	if r.Status != Failed || r.Reason != CloneFailed {
		t.Errorf("got %v %q, want a failed clone", r.Status, r.Reason)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("git ran an askpass program")
	}
}
