// Package azuredevops lists the repositories of an Azure DevOps organization, or of one project.
package azuredevops

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/syntaqx/yoink/internal/gitcred"
	"github.com/syntaqx/yoink/internal/httpx"
	"github.com/syntaqx/yoink/internal/provider"
)

const apiVersion = "7.1"

// Provider is the Azure DevOps provider. The zero value is not usable; call New.
type Provider struct {
	BaseURL string // e.g. https://dev.azure.com
	// Authorization returns the Authorization header value for org.
	Authorization func(ctx context.Context, org string) (string, error)
	HTTP          *http.Client
}

// New returns a provider for dev.azure.com.
func New() *Provider {
	return &Provider{BaseURL: "https://dev.azure.com", Authorization: authorization}
}

func (p *Provider) Info() provider.Info {
	return provider.Info{
		Name:     "ado",
		Aliases:  []string{"azure-devops", "azuredevops"},
		Title:    "Azure DevOps",
		Hosts:    []string{"dev.azure.com"},
		Usage:    "<organization>[/<project>]",
		Auth:     "AZURE_DEVOPS_EXT_PAT, else git's credential helper (sign in once with any HTTPS clone)",
		Protocol: provider.HTTPS,
	}
}

type projectList struct {
	Value []struct {
		Name string `json:"name"`
	} `json:"value"`
}

type repoList struct {
	Value []struct {
		Name          string `json:"name"`
		SSHURL        string `json:"sshUrl"`
		DefaultBranch string `json:"defaultBranch"` // "refs/heads/main"; absent when empty
		IsDisabled    bool   `json:"isDisabled"`
	} `json:"value"`
}

func (p *Provider) Discover(ctx context.Context, target string, progress func(int)) ([]provider.Repo, error) {
	org, only, _ := strings.Cut(target, "/")
	if org == "" || strings.Contains(only, "/") {
		return nil, fmt.Errorf("expected <organization> or <organization>/<project>, got %q", target)
	}
	auth, err := p.Authorization(ctx, org)
	if err != nil {
		return nil, err
	}
	c := &httpx.Client{HTTP: p.HTTP, Header: http.Header{"Authorization": {auth}}}
	orgURL := p.BaseURL + "/" + url.PathEscape(org)

	var projects []string
	for token := ""; ; {
		u := orgURL + "/_apis/projects?api-version=" + apiVersion + "&$top=500"
		if token != "" {
			u += "&continuationToken=" + url.QueryEscape(token)
		}
		var page projectList
		h, err := c.GetJSON(ctx, u, &page)
		if err != nil {
			return nil, fmt.Errorf("listing projects: %w (check the organization name and your sign-in)", err)
		}
		for _, v := range page.Value {
			if only == "" || strings.EqualFold(v.Name, only) {
				projects = append(projects, v.Name)
			}
		}
		if token = h.Get("X-Ms-Continuationtoken"); token == "" {
			break
		}
	}
	if len(projects) == 0 && only != "" {
		return nil, fmt.Errorf("no project named %q in %s", only, org)
	}

	// List each project's repositories concurrently; organizations often have dozens.
	var (
		found   atomic.Int64
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8)
		results = make([][]provider.Repo, len(projects))
		errs    = make([]error, len(projects))
	)
	for i, project := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			projURL := orgURL + "/" + url.PathEscape(project)
			var list repoList
			if _, err := c.GetJSON(ctx, projURL+"/_apis/git/repositories?api-version="+apiVersion, &list); err != nil {
				errs[i] = fmt.Errorf("listing repositories in %s: %w", project, err)
				return
			}
			for _, r := range list.Value {
				repo := provider.Repo{
					Path:          org + "/" + project + "/" + r.Name,
					SSHURL:        r.SSHURL,
					HTTPSURL:      projURL + "/_git/" + url.PathEscape(r.Name),
					DefaultBranch: strings.TrimPrefix(r.DefaultBranch, "refs/heads/"),
				}
				if r.IsDisabled {
					repo.Skip = "disabled"
				}
				results[i] = append(results[i], repo)
			}
			progress(int(found.Add(int64(len(list.Value)))))
		}()
	}
	wg.Wait()

	var repos []provider.Repo
	for i := range projects {
		if errs[i] != nil {
			return nil, errs[i]
		}
		repos = append(repos, results[i]...)
	}
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].Path) < strings.ToLower(repos[j].Path) })
	return repos, nil
}

// authorization prefers a PAT from the environment (the Azure CLI's variable), then the token Git
// Credential Manager stored at sign-in, which is either a PAT (Basic) or an Entra token (Bearer).
func authorization(ctx context.Context, org string) (string, error) {
	if pat := strings.TrimSpace(os.Getenv("AZURE_DEVOPS_EXT_PAT")); pat != "" {
		return basic(pat), nil
	}
	tok := gitcred.Fill(ctx, "dev.azure.com", org)
	if tok == "" {
		return "", fmt.Errorf("no Azure DevOps credential found: clone any repository in %s over HTTPS once to sign in, or set AZURE_DEVOPS_EXT_PAT to a Code (Read) token", org)
	}
	if strings.HasPrefix(tok, "eyJ") { // a JWT
		return "Bearer " + tok, nil
	}
	return basic(tok), nil
}

func basic(pat string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat))
}
