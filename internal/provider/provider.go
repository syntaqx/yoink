// Package provider defines what a hosting service supplies to yoink: a way to list the
// repositories behind a target. Cloning, updating and reporting are shared and live elsewhere.
package provider

import (
	"context"
	"strings"
)

// Protocol is the transport a repository is cloned over.
type Protocol string

const (
	SSH   Protocol = "ssh"
	HTTPS Protocol = "https"
)

// Info describes a provider for argument parsing and help output.
type Info struct {
	Name     string   // canonical command name, e.g. "github"
	Aliases  []string // other accepted names, e.g. "gh"
	Title    string   // display name, e.g. "GitHub"
	Hosts    []string // hosts whose URLs select this provider, e.g. "github.com"
	Usage    string   // target syntax, e.g. "<owner>"
	Auth     string   // one line on where credentials come from
	Protocol Protocol // default clone protocol
}

// Repo is one repository a provider found.
type Repo struct {
	// Path is where the clone lives relative to the root, slash-separated: "owner/repo".
	Path     string
	SSHURL   string
	HTTPSURL string
	// DefaultBranch is the branch kept checked out; empty when the repository has no commits.
	DefaultBranch string
	// Skip, when non-empty, is why the repository can't be synced (e.g. "disabled"). Skipped
	// repositories are counted in the summary but never touched.
	Skip string
}

// URL returns the clone URL for p, falling back to the other protocol if p has none.
func (r Repo) URL(p Protocol) string {
	if p == HTTPS && r.HTTPSURL != "" || r.SSHURL == "" {
		return r.HTTPSURL
	}
	return r.SSHURL
}

// OtherURL returns the clone URL for the protocol that isn't p, or "" if there isn't one.
func (r Repo) OtherURL(p Protocol) string {
	if u := r.URL(p); u == r.SSHURL {
		return r.HTTPSURL
	}
	return r.SSHURL
}

// Provider lists the repositories behind a target, such as an organization.
type Provider interface {
	Info() Info
	// Discover returns every repository in target. It calls progress, possibly from several
	// goroutines, with the running count of repositories found so far.
	Discover(ctx context.Context, target string, progress func(found int)) ([]Repo, error)
}

// Registry is the set of known providers.
type Registry []Provider

// Lookup returns the provider named name (or one of its aliases), ignoring case.
func (r Registry) Lookup(name string) Provider {
	for _, p := range r {
		info := p.Info()
		if strings.EqualFold(info.Name, name) {
			return p
		}
		for _, a := range info.Aliases {
			if strings.EqualFold(a, name) {
				return p
			}
		}
	}
	return nil
}

// ForHost returns the provider serving host, ignoring case and a "www." prefix.
func (r Registry) ForHost(host string) Provider {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, p := range r {
		for _, h := range p.Info().Hosts {
			if strings.EqualFold(h, host) {
				return p
			}
		}
	}
	return nil
}
