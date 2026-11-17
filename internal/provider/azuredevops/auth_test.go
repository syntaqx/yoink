package azuredevops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// credential makes git's credential helper answer https://dev.azure.com/<org> with password, or
// with nothing when password is "". It returns a file recording what the helper was asked.
func credential(t *testing.T, password string) (requests string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cfg, requests := filepath.Join(dir, "gitconfig"), filepath.Join(dir, "requests")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if password == "" {
		return requests
	}
	helper := `!f() { test "$1" = get || exit 0; cat >> '` + filepath.ToSlash(requests) + `'; ` +
		`echo username=u; echo password=` + password + `; }; f`
	for _, kv := range [][2]string{{"credential.helper", helper}, {"credential.useHttpPath", "true"}} {
		if out, err := exec.Command("git", "config", "--file", cfg, kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v\n%s", err, out)
		}
	}
	return requests
}

func TestAuthorization(t *testing.T) {
	const jwt = "eyJhbGciOiJSUzI1NiJ9.payload.sig"
	for _, c := range []struct {
		name, pat, stored, want string
	}{
		{"PAT from the environment", "secret", "stored", basic("secret")},
		{"PAT is trimmed", "  secret\n", "", basic("secret")},
		{"blank PAT falls back to git", "   ", "stored-pat", basic("stored-pat")},
		{"stored Entra token is a bearer token", "", jwt, "Bearer " + jwt},
		{"stored PAT is basic auth", "", "stored-pat", basic("stored-pat")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("AZURE_DEVOPS_EXT_PAT", c.pat)
			credential(t, c.stored)
			got, err := authorization(context.Background(), "contoso")
			if err != nil || got != c.want {
				t.Errorf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestAuthorizationAsksGitForTheOrganization(t *testing.T) {
	t.Setenv("AZURE_DEVOPS_EXT_PAT", "")
	requests := credential(t, "stored-pat")
	if _, err := authorization(context.Background(), "contoso"); err != nil {
		t.Fatal(err)
	}
	// GCM stores Azure DevOps credentials per organization, so the path must be sent.
	if b, _ := os.ReadFile(requests); string(b) != "protocol=https\nhost=dev.azure.com\npath=contoso\n" {
		t.Errorf("helper was asked %q", b)
	}
}

func TestAuthorizationWithoutCredentials(t *testing.T) {
	t.Setenv("AZURE_DEVOPS_EXT_PAT", "")
	credential(t, "")
	_, err := authorization(context.Background(), "contoso")
	if err == nil || !strings.Contains(err.Error(), "AZURE_DEVOPS_EXT_PAT") || !strings.Contains(err.Error(), "in contoso") {
		t.Fatalf("err = %v, want one naming the organization and AZURE_DEVOPS_EXT_PAT", err)
	}
}

func TestBasic(t *testing.T) {
	got := basic("pat")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Basic "))
	if !strings.HasPrefix(got, "Basic ") || err != nil || string(raw) != ":pat" {
		t.Errorf("basic(pat) = %q (decodes to %q, %v), want Basic base64(\":pat\")", got, raw, err)
	}
}

func TestNew(t *testing.T) {
	p := New()
	info := p.Info()
	if p.BaseURL != "https://dev.azure.com" || p.Authorization == nil {
		t.Errorf("New = %+v", p)
	}
	if info.Name != "ado" || len(info.Hosts) != 1 || info.Hosts[0] != "dev.azure.com" || info.Protocol != "https" {
		t.Errorf("Info = %+v", info)
	}
}

func TestDiscoverRejectsBadTargets(t *testing.T) {
	var calls atomic.Int32
	p := &Provider{BaseURL: "http://unused.invalid", Authorization: func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", nil
	}}
	for _, target := range []string{"", "/Website", "contoso/Website/extra"} {
		if _, err := p.Discover(context.Background(), target, func(int) {}); err == nil || !strings.Contains(err.Error(), "expected <organization>") {
			t.Errorf("Discover(%q): err = %v", target, err)
		}
	}
	if calls.Load() != 0 {
		t.Error("looked up credentials for an invalid target")
	}
}

func TestDiscoverReportsAuthorizationErrors(t *testing.T) {
	want := errors.New("no credential")
	p := &Provider{BaseURL: "http://unused.invalid", Authorization: func(_ context.Context, org string) (string, error) {
		if org != "contoso" {
			t.Errorf("asked for %q's credentials", org)
		}
		return "", want
	}}
	if _, err := p.Discover(context.Background(), "contoso/Website", func(int) {}); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestDiscoverReportsProjectErrors(t *testing.T) {
	p := newServer(t)
	p.BaseURL += "/missing" // every request 404s
	_, err := p.Discover(context.Background(), "contoso", func(int) {})
	if err == nil || !strings.HasPrefix(err.Error(), "listing projects: HTTP 404") || !strings.Contains(err.Error(), "check the organization name") {
		t.Errorf("err = %v", err)
	}
}

func TestDiscoverReportsRepositoryErrors(t *testing.T) {
	// Projects list fine, but two projects' repositories can't be read. The first failing
	// project, in listing order, is the one reported, however the requests interleave.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/contoso/_apis/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]string{{"name": "Fine"}, {"name": "Broken One"}, {"name": "Broken Two"}}})
		case "/contoso/Fine/_apis/git/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{"name": "ok"}}})
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "TF400813: not authorized"})
		}
	}))
	t.Cleanup(srv.Close)
	p := &Provider{BaseURL: srv.URL, Authorization: func(context.Context, string) (string, error) { return "Basic x", nil }}
	for range 5 {
		_, err := p.Discover(context.Background(), "contoso", func(int) {})
		if err == nil || err.Error() != "listing repositories in Broken One: HTTP 401: TF400813: not authorized" {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestDiscoverProgressCountsEveryRepository(t *testing.T) {
	p := newServer(t)
	var last atomic.Int64
	repos, err := p.Discover(context.Background(), "contoso", func(n int) {
		for {
			old := last.Load()
			if int64(n) <= old || last.CompareAndSwap(old, int64(n)) {
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if int(last.Load()) != len(repos) {
		t.Errorf("progress peaked at %d, found %d", last.Load(), len(repos))
	}
}
