package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer(t *testing.T) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic test" {
			// What Azure DevOps really does without credentials: 203 and a sign-in page.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNonAuthoritativeInfo)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.EscapedPath() {
		case "/contoso/_apis/projects":
			if r.URL.Query().Get("continuationToken") == "" {
				w.Header().Set("X-Ms-Continuationtoken", "next")
				reply(map[string]any{"value": []map[string]string{{"name": "Website"}}})
				return
			}
			reply(map[string]any{"value": []map[string]string{{"name": "My Project"}}})
		case "/contoso/Website/_apis/git/repositories":
			reply(map[string]any{"value": []map[string]any{
				{"name": "web", "defaultBranch": "refs/heads/main", "sshUrl": "git@ssh.dev.azure.com:v3/contoso/Website/web"},
				{"name": "old", "isDisabled": true},
			}})
		case "/contoso/My%20Project/_apis/git/repositories":
			reply(map[string]any{"value": []map[string]any{{"name": "empty"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Provider{
		BaseURL:       srv.URL,
		Authorization: func(context.Context, string) (string, error) { return "Basic test", nil },
	}
}

func TestDiscoverOrganization(t *testing.T) {
	p := newServer(t)
	repos, err := p.Discover(context.Background(), "contoso", func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range repos {
		got = append(got, fmt.Sprintf("%s|%s|%s", r.Path, r.DefaultBranch, r.Skip))
	}
	want := "[contoso/My Project/empty|| contoso/Website/old||disabled contoso/Website/web|main|]"
	if fmt.Sprint(got) != want {
		t.Errorf("got  %v\nwant %s", got, want)
	}
	web := repos[2]
	if !strings.HasSuffix(web.HTTPSURL, "/contoso/Website/_git/web") || web.SSHURL == "" {
		t.Errorf("web = %+v", web)
	}
	if !strings.HasSuffix(repos[0].HTTPSURL, "/contoso/My%20Project/_git/empty") {
		t.Errorf("project with a space: %s", repos[0].HTTPSURL)
	}
}

func TestDiscoverOneProject(t *testing.T) {
	p := newServer(t)
	repos, err := p.Discover(context.Background(), "contoso/website", func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Errorf("got %d repos, want 2", len(repos))
	}
	if _, err := p.Discover(context.Background(), "contoso/Nope", func(int) {}); err == nil || !strings.Contains(err.Error(), "no project") {
		t.Errorf("err = %v", err)
	}
}

func TestSignInPageIsAnError(t *testing.T) {
	p := newServer(t)
	p.Authorization = func(context.Context, string) (string, error) { return "Basic wrong", nil }
	_, err := p.Discover(context.Background(), "contoso", func(int) {})
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthorizationFromPAT(t *testing.T) {
	t.Setenv("AZURE_DEVOPS_EXT_PAT", "secret")
	got, err := authorization(context.Background(), "contoso")
	if err != nil || got != "Basic OnNlY3JldA==" { // base64(":secret")
		t.Fatalf("got %q, %v", got, err)
	}
}
