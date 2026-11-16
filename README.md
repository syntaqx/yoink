# yoink

Grab every repository in an organization and keep your local copies up to date: helper scripts that
clone what's missing and pull what's there, so a whole org stays current without cloning each repo
by hand:

- `github.sh` for GitHub organizations and users
- `ado.sh` for Azure DevOps organizations

Everything is cloned under `~/Code`, one directory per organization (and, for Azure DevOps, one per
project inside it), so updating any of them is one command from this directory:

```text
~/Code/
├── <github-org>/<repo>
└── <ado-org>/<project>/<repo>
```

Both scripts discover every repository first, then sync them in parallel with live `[n/total]`
progress: existing clones check out their default branch and pull, new ones are cloned. Anything
that fails is listed at the end, and repos with local changes can be reset and retried.

## Prerequisites

- `git`, `jq` and `curl`.

### `github.sh`

- A `GITHUB_TOKEN` for the API, to list the organization's repositories.
- SSH access to GitHub (`git clone git@github.com:…`); existing HTTPS remotes are switched to SSH.

### `ado.sh`

- A sign-in with Git Credential Manager (bundled with Git for Windows): clone any repository in the
  organization over HTTPS once and sign in when prompted. The script reuses that credential for the
  Azure DevOps API, so there's no token to manage.
- Or set `AZURE_DEVOPS_EXT_PAT` to a personal access token with **Code (Read)** scope; it takes
  precedence when set.
- Clones are HTTPS (`https://dev.azure.com/<org>/<project>/_git/<repo>`); no SSH keys needed.

## Usage

```bash
./github.sh <organization|username> [-v|--verbose]

./ado.sh <organization> [project] [-v|--verbose]
./ado.sh DestPetDev               # every project in the organization
./ado.sh DestPetDev DEVOPS-Corp   # just one project
```

Set `MAX_PARALLEL_JOBS` (default 5) to change how many repositories sync at once. Repositories can
be skipped by adding them to `IGNORE_LIST` in either script (`<project>/<repo>` for `ado.sh`).
Disabled Azure DevOps repositories are skipped automatically.
