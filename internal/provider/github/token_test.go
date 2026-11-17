package github

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeGH puts a stand-in gh at the front of PATH, ahead of any real one (the rest of PATH stays,
// since git needs it to run credential helpers). script is what it prints for
// `gh auth token --hostname github.com`; it exits non-zero if ok is false.
func fakeGH(t *testing.T, script string, ok bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		body := "@echo off\r\nif not \"%*\"==\"auth token --hostname github.com\" exit /b 2\r\n"
		if script != "" {
			body += "echo " + script + "\r\n"
		}
		if !ok {
			body += "exit /b 1\r\n"
		}
		write(t, filepath.Join(dir, "gh.bat"), body)
	} else {
		body := "#!/bin/sh\n[ \"$*\" = \"auth token --hostname github.com\" ] || exit 2\n"
		if script != "" {
			body += "echo " + script + "\n"
		}
		if !ok {
			body += "exit 1\n"
		}
		write(t, filepath.Join(dir, "gh"), body)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// credential makes git's credential helper answer github.com with password, or with nothing
// when password is "".
func credential(t *testing.T, password string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	write(t, cfg, "")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if password == "" {
		return
	}
	helper := `!f() { test "$1" = get && echo username=u && echo password=` + password + `; }; f`
	if out, err := exec.Command("git", "config", "--file", cfg, "credential.helper", helper).CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestTokenPrecedence(t *testing.T) {
	for _, c := range []struct {
		name               string
		githubToken, ghTok string
		gh                 string
		ghOK               bool
		stored             string
		want               string
	}{
		{"GITHUB_TOKEN first", " env-1\n", "env-2", "gh-tok", true, "stored", "env-1"},
		{"then GH_TOKEN", "", " env-2 ", "gh-tok", true, "stored", "env-2"},
		{"blank env is skipped", "  ", "", "gh-tok", true, "stored", "gh-tok"},
		{"gh failing falls through", "", "", "gh-tok", false, "stored", "stored"},
		{"gh printing nothing falls through", "", "", "", true, "stored", "stored"},
		{"nothing anywhere", "", "", "", false, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", c.githubToken)
			t.Setenv("GH_TOKEN", c.ghTok)
			fakeGH(t, c.gh, c.ghOK)
			credential(t, c.stored)
			if got := token(context.Background()); got != c.want {
				t.Errorf("token = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNewUsesGitHubAndItsToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from-env")
	p := New()
	if p.API != "https://api.github.com" {
		t.Errorf("API = %q", p.API)
	}
	if got := p.Token(context.Background()); got != "from-env" {
		t.Errorf("Token = %q, want the environment's", got)
	}
	info := p.Info()
	if info.Name != "github" || len(info.Hosts) != 1 || info.Hosts[0] != "github.com" || info.Protocol != "ssh" {
		t.Errorf("Info = %+v", info)
	}
}

func TestNoTokenSendsNoAuthorization(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("Authorization = %q without a token", auth)
		}
		switch r.URL.Path {
		case "/users/someone":
			reply(w, account{Login: "someone", Type: "User"})
		case "/user":
			t.Error("asked who the authenticated user is without a token")
		case "/users/someone/repos":
			reply(w, page("public-one"))
		default:
			http.NotFound(w, r)
		}
	})
	p.Token = func(context.Context) string { return "" }
	if repos, err := p.Discover(context.Background(), "someone", func(int) {}); err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v, err = %v", repos, err)
	}
	p.Token = nil // a provider without a token source works the same
	if _, err := p.Discover(context.Background(), "someone", func(int) {}); err != nil {
		t.Fatal(err)
	}
}

func TestLaterPageFailureIsExplained(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, base string) {
		switch {
		case r.URL.Path == "/users/acme":
			reply(w, account{Login: "Acme", Type: "Organization"})
		case r.URL.Query().Get("page") == "2":
			w.WriteHeader(http.StatusForbidden)
			reply(w, map[string]string{"message": "Resource protected by organization SAML enforcement"})
		default:
			w.Header().Set("Link", "<"+base+`/orgs/Acme/repos?page=2>; rel="next"`)
			reply(w, page("alpha"))
		}
	})
	_, err := p.Discover(context.Background(), "acme", func(int) {})
	want := `HTTP 403: Resource protected by organization SAML enforcement (check that your token has access to "Acme")`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

func TestUnexplainedErrorsPassThrough(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusBadGateway)
		reply(w, map[string]string{"message": "Server Error"})
	})
	if _, err := p.Discover(context.Background(), "acme", func(int) {}); err == nil || err.Error() != "HTTP 502: Server Error" {
		t.Errorf("err = %v", err)
	}

	p = serve(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte("{not json"))
	})
	_, err := p.Discover(context.Background(), "acme", func(int) {})
	if err == nil || !strings.HasPrefix(err.Error(), "decoding ") || strings.Contains(err.Error(), "check that") {
		t.Errorf("err = %v, want the decoding error without a hint", err)
	}
}

func TestOwnerMustBeOneName(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		t.Errorf("no request should be made, got %s", r.URL)
	})
	for _, owner := range []string{"", "acme/repo", "two words"} {
		if _, err := p.Discover(context.Background(), owner, func(int) {}); err == nil {
			t.Errorf("Discover(%q): want an error", owner)
		}
	}
}
