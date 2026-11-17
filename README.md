# yoink

[![CI](https://github.com/syntaqx/yoink/actions/workflows/ci.yml/badge.svg)](https://github.com/syntaqx/yoink/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/syntaqx/yoink.svg)](https://pkg.go.dev/github.com/syntaqx/yoink)

Grab every repository in an organization and keep your local copies up to date. yoink clones
what's missing and fast-forwards what's there, so a whole org stays current with one command.

```text
~/Code/
├── <github-owner>/<repo>
└── <ado-org>/<project>/<repo>
```

Supported hosts: **GitHub** (organizations and users) and **Azure DevOps** (organizations or a
single project).

## Install

```bash
go install github.com/syntaqx/yoink/cmd/yoink@latest
```

This puts `yoink` in `$(go env GOPATH)/bin`; make sure that's on your `PATH`. You need `git` too.

Prebuilt binaries for Linux, macOS and Windows (amd64 and arm64) are attached to each
[release](https://github.com/syntaqx/yoink/releases), with checksums and signed build provenance:

```bash
gh attestation verify yoink_*.tar.gz --repo syntaqx/yoink
```

## Usage

```bash
yoink github acme                  # every repository owned by acme (org or user)
yoink gh acme acme-labs            # several owners at once
yoink ado contoso                  # every project in an Azure DevOps organization
yoink ado contoso/Website          # one project
yoink https://github.com/acme      # or just paste URLs; the host picks the provider
yoink gh acme --dry-run            # see what would be cloned or synced
```

yoink runs the same from any directory. It discovers every repository first, then syncs them in
parallel with live `[n/total]` progress:

- **Missing** repositories are cloned.
- **Existing** clones are fetched, switched to their default branch and **fast-forwarded only**.
  yoink never merges, rebases or discards anything on its own. A clone with local commits is
  reported as *diverged*, and one whose uncommitted changes block the update is reported as
  *local changes* and left untouched.
- At the end, yoink offers to `git reset --hard` the repositories with local changes and retry.
  It asks first, and only on an interactive terminal.

Archived repositories are synced like any other; disabled Azure DevOps repositories are skipped.

### Flags

| Flag | Env | Default | |
| --- | --- | --- | --- |
| `-r, --root DIR` | `YOINK_ROOT` | `~/Code` | where clones live |
| `-j, --jobs N` | `YOINK_JOBS` | `8` | repositories synced at once |
| `-p, --protocol ssh\|https` | `YOINK_PROTOCOL` | per provider | clone protocol |
| `-i, --ignore GLOB` | | | skip matching repositories (repeatable) |
| `-n, --dry-run` | | | show the plan, change nothing |
| `-v, --verbose` | | | show git's output for failures |
| `--no-color` | `NO_COLOR` | | plain output |

Flags can go before or after the targets. When the protocol is switched, existing clones whose
`origin` uses the other protocol are repointed; custom remotes are left alone.

### Ignoring repositories

Ignore patterns are globs, one per line, matched case-insensitively against the path under the
root. A pattern that matches a directory skips everything in it. Patterns come from three
places, and they add up:

| Source | Applies to |
| --- | --- |
| `~/.config/yoink/ignore` (or `$XDG_CONFIG_HOME/yoink/ignore`) | every root; the same path on Windows |
| `<root>/.yoinkignore` | that root |
| `--ignore GLOB` | that run |

```text
# ~/.config/yoink/ignore: habits that hold everywhere
*/*-deprecated
*/sandbox

# ~/Code/.yoinkignore: specific to these clones
acme/legacy-*
contoso/OldProject
```

## Authentication

| Provider | Default protocol | Credentials, in order |
| --- | --- | --- |
| GitHub | SSH | `GITHUB_TOKEN`, `GH_TOKEN`, `gh auth token`, git's credential helper |
| Azure DevOps | HTTPS | `AZURE_DEVOPS_EXT_PAT`, git's credential helper |

GitHub needs a token only to list private repositories (and to avoid rate limits). Cloning uses
your SSH key, or your git credentials with `--protocol https`.

For Azure DevOps, the simplest setup is Git Credential Manager (bundled with Git for Windows):
clone any repository in the organization over HTTPS once and sign in when prompted, and yoink
reuses that credential. Otherwise, set `AZURE_DEVOPS_EXT_PAT` to a personal access token with
**Code (Read)** scope.

## Adding a provider

A provider only lists repositories; cloning, syncing, progress and reporting are shared. To add
one:

1. Create `internal/provider/<name>/` with a type implementing `provider.Provider`: `Info()`
   (name, aliases, hosts, target syntax, auth hint, default protocol) and `Discover()`, which
   returns each repository's path under the root, its SSH and HTTPS clone URLs, and its default
   branch.
2. Add it to `registry()` in `internal/cli/cli.go`.

It then gets its own subcommand, URL detection for its hosts, and help text automatically. See
`internal/provider/github` for a compact example, with tests against a fake API server.

## Development

```bash
go test ./...
go run ./cmd/yoink gh acme --dry-run
golangci-lint run          # config in .golangci.yml
goreleaser check           # config in .goreleaser.yaml
```

CI lints, tests with the race detector on Linux, macOS and Windows, runs `govulncheck` (also
weekly), and does a GoReleaser dry run. Pushing a `v*` tag publishes a release. Dependabot keeps
the actions (on major-version tags) and Go modules current.
