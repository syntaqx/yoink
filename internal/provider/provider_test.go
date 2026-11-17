package provider

import (
	"context"
	"testing"
)

func TestRepoURL(t *testing.T) {
	const ssh, https = "git@host:o/r.git", "https://host/o/r.git"
	for _, c := range []struct {
		name          string
		repo          Repo
		proto         Protocol
		url, otherURL string
	}{
		{"both, ssh", Repo{SSHURL: ssh, HTTPSURL: https}, SSH, ssh, https},
		{"both, https", Repo{SSHURL: ssh, HTTPSURL: https}, HTTPS, https, ssh},
		{"ssh only, ssh", Repo{SSHURL: ssh}, SSH, ssh, ""},
		{"ssh only, https falls back", Repo{SSHURL: ssh}, HTTPS, ssh, ""},
		{"https only, https", Repo{HTTPSURL: https}, HTTPS, https, ""},
		{"https only, ssh falls back", Repo{HTTPSURL: https}, SSH, https, ""},
		{"neither", Repo{}, SSH, "", ""},
	} {
		if got := c.repo.URL(c.proto); got != c.url {
			t.Errorf("%s: URL = %q, want %q", c.name, got, c.url)
		}
		if got := c.repo.OtherURL(c.proto); got != c.otherURL {
			t.Errorf("%s: OtherURL = %q, want %q", c.name, got, c.otherURL)
		}
	}
}

type fake Info

func (f *fake) Info() Info { return Info(*f) }
func (*fake) Discover(context.Context, string, func(int)) ([]Repo, error) {
	return nil, nil
}

func TestRegistry(t *testing.T) {
	hub := &fake{Name: "github", Aliases: []string{"gh"}, Hosts: []string{"github.com"}}
	ado := &fake{Name: "ado", Aliases: []string{"azure-devops"}, Hosts: []string{"dev.azure.com"}}
	reg := Registry{hub, ado}

	for name, want := range map[string]Provider{
		"github": hub, "GitHub": hub, "gh": hub, "GH": hub,
		"ado": ado, "Azure-DevOps": ado,
		"gitlab": nil, "": nil, "github.com": nil,
	} {
		if got := reg.Lookup(name); got != want {
			t.Errorf("Lookup(%q) = %v, want %v", name, got, want)
		}
	}
	for host, want := range map[string]Provider{
		"github.com": hub, "GitHub.COM": hub, "www.github.com": hub, "WWW.GitHub.com": hub,
		"dev.azure.com":   ado,
		"gist.github.com": nil, "github": nil, "": nil, "www.": nil,
	} {
		if got := reg.ForHost(host); got != want {
			t.Errorf("ForHost(%q) = %v, want %v", host, got, want)
		}
	}
	if (Registry{}).Lookup("github") != nil || (Registry{}).ForHost("github.com") != nil {
		t.Error("an empty registry found a provider")
	}
}
