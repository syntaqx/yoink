package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syntaqx/yoink/internal/httpx"
)

func serve(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, base string)) *Provider {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		handler(w, r, srv.URL)
	}))
	t.Cleanup(srv.Close)
	return &Provider{API: srv.URL, Token: func(context.Context) string { return "tok" }}
}

func reply(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func page(names ...string) []repository {
	var out []repository
	for _, n := range names {
		out = append(out, repository{
			Name: n, DefaultBranch: "main",
			SSHURL: "git@github.com:Acme/" + n + ".git", CloneURL: "https://github.com/Acme/" + n + ".git",
		})
	}
	return out
}

func TestOrganizationPaginates(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, base string) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing token on %s", r.URL)
		}
		switch r.URL.Path {
		case "/users/acme":
			reply(w, account{Login: "Acme", Type: "Organization"})
		case "/orgs/Acme/repos":
			if r.URL.Query().Get("page") == "2" {
				reply(w, page("beta"))
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/orgs/Acme/repos?page=2>; rel="next", <%s/orgs/Acme/repos?page=2>; rel="last"`, base, base))
			reply(w, page("gamma", "alpha"))
		default:
			http.NotFound(w, r)
		}
	})

	var progress []int
	repos, err := p.Discover(context.Background(), "acme", func(n int) { progress = append(progress, n) })
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range repos {
		paths = append(paths, r.Path)
	}
	if fmt.Sprint(paths) != "[Acme/alpha Acme/beta Acme/gamma]" {
		t.Errorf("paths = %v", paths)
	}
	if fmt.Sprint(progress) != "[2 3]" {
		t.Errorf("progress = %v", progress)
	}
	if repos[0].SSHURL != "git@github.com:Acme/alpha.git" || repos[0].DefaultBranch != "main" {
		t.Errorf("repo = %+v", repos[0])
	}
}

func TestAuthenticatedUserListsOwnRepos(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.URL.Path {
		case "/users/me":
			reply(w, account{Login: "me", Type: "User"})
		case "/user":
			reply(w, account{Login: "me", Type: "User"})
		case "/user/repos":
			if r.URL.Query().Get("visibility") != "all" {
				t.Errorf("own listing should include private repos: %s", r.URL)
			}
			reply(w, page("private-one"))
		default:
			http.NotFound(w, r)
		}
	})
	repos, err := p.Discover(context.Background(), "me", func(int) {})
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v, err = %v", repos, err)
	}
}

func TestOtherUser(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.URL.Path {
		case "/users/someone":
			reply(w, account{Login: "someone", Type: "User"})
		case "/user":
			reply(w, account{Login: "me", Type: "User"})
		case "/users/someone/repos":
			reply(w, page("public-one"))
		default:
			http.NotFound(w, r)
		}
	})
	if repos, err := p.Discover(context.Background(), "someone", func(int) {}); err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v, err = %v", repos, err)
	}
}

func TestErrorsCarryTheAPIMessage(t *testing.T) {
	p := serve(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		w.WriteHeader(http.StatusNotFound)
		reply(w, map[string]string{"message": "Not Found"})
	})
	_, err := p.Discover(context.Background(), "nobody", func(int) {})
	want := `HTTP 404: Not Found (check that "nobody" is a GitHub organization or user you can see)`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	var he *httpx.Error
	if !errors.As(err, &he) || he.StatusCode != http.StatusNotFound {
		t.Errorf("want the *httpx.Error kept in the chain, got %T", err)
	}
	if _, err := p.Discover(context.Background(), "acme/repo", func(int) {}); err == nil {
		t.Error("want an error for a slash in the owner")
	}
}

func TestErrorHints(t *testing.T) {
	status := http.StatusUnauthorized
	p := serve(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		w.WriteHeader(status)
		reply(w, map[string]string{"message": "Bad credentials"})
	})
	if _, err := p.Discover(context.Background(), "acme", func(int) {}); err == nil || !strings.Contains(err.Error(), "GitHub rejected the token") {
		t.Errorf("401: err = %v", err)
	}

	status = http.StatusForbidden
	if _, err := p.Discover(context.Background(), "acme", func(int) {}); err == nil || !strings.Contains(err.Error(), `token has access to "acme"`) {
		t.Errorf("403 with a token: err = %v", err)
	}
	p.Token = func(context.Context) string { return "" }
	if _, err := p.Discover(context.Background(), "acme", func(int) {}); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("403 without a token: err = %v", err)
	}
}

func TestNextLink(t *testing.T) {
	h := `<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next"`
	if got := nextLink(h); got != "https://api.github.com/x?page=3" {
		t.Errorf("nextLink = %q", got)
	}
	if got := nextLink(`<https://api.github.com/x?page=1>; rel="prev"`); got != "" {
		t.Errorf("nextLink = %q, want empty", got)
	}
}
