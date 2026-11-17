// Package github lists the repositories of a GitHub organization or user.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/syntaqx/yoink/internal/gitcred"
	"github.com/syntaqx/yoink/internal/httpx"
	"github.com/syntaqx/yoink/internal/provider"
)

// Provider is the GitHub provider. The zero value is not usable; call New.
type Provider struct {
	API string // API base URL
	// Token returns the API token; "" means unauthenticated (public repositories only).
	Token func(ctx context.Context) string
	HTTP  *http.Client
}

// New returns a provider for github.com.
func New() *Provider {
	return &Provider{API: "https://api.github.com", Token: token}
}

func (p *Provider) Info() provider.Info {
	return provider.Info{
		Name:     "github",
		Aliases:  []string{"gh"},
		Title:    "GitHub",
		Hosts:    []string{"github.com"},
		Usage:    "<owner>",
		Auth:     "GITHUB_TOKEN or GH_TOKEN, else `gh auth token`, else git's credential helper",
		Protocol: provider.SSH,
	}
}

type account struct {
	Login string `json:"login"`
	Type  string `json:"type"` // "User" or "Organization"
}

type repository struct {
	Name          string `json:"name"`
	SSHURL        string `json:"ssh_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

func (p *Provider) Discover(ctx context.Context, owner string, progress func(int)) ([]provider.Repo, error) {
	if owner == "" || strings.ContainsAny(owner, "/ ") {
		return nil, fmt.Errorf("expected an organization or user name, got %q", owner)
	}

	c := &httpx.Client{HTTP: p.HTTP, Header: http.Header{
		"Accept":               {"application/vnd.github+json"},
		"X-Github-Api-Version": {"2022-11-28"},
	}}
	tok := ""
	if p.Token != nil {
		tok = p.Token(ctx)
	}
	if tok != "" {
		c.Header.Set("Authorization", "Bearer "+tok)
	}

	var acct account
	if _, err := c.GetJSON(ctx, p.API+"/users/"+url.PathEscape(owner), &acct); err != nil {
		return nil, explain(err, owner, tok != "")
	}

	// Organizations list everything the token can see. For users, only the authenticated user's
	// own listing includes their private repositories.
	list := p.API + "/users/" + url.PathEscape(acct.Login) + "/repos?type=owner&per_page=100"
	if acct.Type == "Organization" {
		list = p.API + "/orgs/" + url.PathEscape(acct.Login) + "/repos?type=all&per_page=100"
	} else if tok != "" {
		var me account
		if _, err := c.GetJSON(ctx, p.API+"/user", &me); err == nil && strings.EqualFold(me.Login, acct.Login) {
			list = p.API + "/user/repos?affiliation=owner&visibility=all&per_page=100"
		}
	}

	var repos []provider.Repo
	for next := list; next != ""; {
		var page []repository
		h, err := c.GetJSON(ctx, next, &page)
		if err != nil {
			return nil, explain(err, acct.Login, tok != "")
		}
		for _, r := range page {
			repos = append(repos, provider.Repo{
				Path:          acct.Login + "/" + r.Name,
				SSHURL:        r.SSHURL,
				HTTPSURL:      r.CloneURL,
				DefaultBranch: r.DefaultBranch,
			})
		}
		progress(len(repos))
		next = nextLink(h.Get("Link"))
	}
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].Path) < strings.ToLower(repos[j].Path) })
	return repos, nil
}

// explain adds a hint for the API errors people hit most: a misspelled owner and a bad token.
func explain(err error, owner string, authed bool) error {
	var he *httpx.Error
	if !errors.As(err, &he) {
		return err
	}
	switch he.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w (check that %q is a GitHub organization or user you can see)", err, owner)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (GitHub rejected the token; check GITHUB_TOKEN, GH_TOKEN or `gh auth status`)", err)
	case http.StatusForbidden:
		if !authed {
			return fmt.Errorf("%w (set GITHUB_TOKEN or run `gh auth login` to authenticate)", err)
		}
		return fmt.Errorf("%w (check that your token has access to %q)", err, owner)
	}
	return err
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextLink returns the rel="next" URL from a Link header, or "".
func nextLink(header string) string {
	if m := linkNext.FindStringSubmatch(header); m != nil {
		return m[1]
	}
	return ""
}

// token finds a GitHub token the way the gh CLI does, then falls back to git's credentials.
func token(ctx context.Context) string {
	for _, env := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	if gh, err := exec.LookPath("gh"); err == nil {
		if out, err := exec.CommandContext(ctx, gh, "auth", "token", "--hostname", "github.com").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v
			}
		}
	}
	return gitcred.Fill(ctx, "github.com", "")
}
