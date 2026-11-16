#!/bin/bash

# ado.sh: keep every repository in an Azure DevOps organization cloned and up to date,
# laid out as <organization>/<project>/<repo> under the current directory.
#
# Auth: uses the credential Git Credential Manager already holds for dev.azure.com (sign in once by
# cloning any repo over HTTPS), or AZURE_DEVOPS_EXT_PAT if it's set (the Azure CLI's variable).

if [ -z "$1" ]; then
  echo "Usage: $0 <organization> [project] [-v|--verbose]"
  echo "       e.g. $0 DestPetDev            (every project)"
  echo "            $0 DestPetDev DEVOPS-Corp (one project)"
  exit 1
fi

# Strip surrounding whitespace and slashes, as github.sh does.
ORG=$(echo "$1" | sed -E 's#^[[:space:]/]+|[[:space:]/]+$##g')
ONLY_PROJECT=""
VERBOSE=false
for arg in "${@:2}"; do
  case "$arg" in
    -v | --verbose) VERBOSE=true ;;
    *) ONLY_PROJECT=$(echo "$arg" | sed -E 's#^[[:space:]/]+|[[:space:]/]+$##g') ;;
  esac
done
if [ -z "$ORG" ]; then
  echo "Error: invalid organization name '$1'."
  exit 1
fi

for cmd in git curl jq; do
  command -v "$cmd" > /dev/null || { echo "Error: $cmd is required."; exit 1; }
done

ADO_URL="https://dev.azure.com/$ORG"
API_VERSION="7.1"
ORG_DIR=$ORG
TOTAL_REPOS=0
SKIPPED_REPOS=0
DISABLED_REPOS=0
SYNCED_REPOS=0
CLONED_REPOS=0
MAX_PARALLEL_JOBS=${MAX_PARALLEL_JOBS:-5}
declare -a FAILED_REPOS
RESULTS_FILE="/tmp/ado_sync_$$.tmp"
COUNT_WIDTH=1

# Colors — automatically disabled when stdout is not a terminal (e.g. piped)
if [ -t 1 ]; then
  BOLD=$'\e[1m'; DIM=$'\e[2m'; RESET=$'\e[0m'
  RED=$'\e[31m'; GREEN=$'\e[32m'; YELLOW=$'\e[33m'
  BLUE=$'\e[34m'; MAGENTA=$'\e[35m'; CYAN=$'\e[36m'; GREY=$'\e[90m'
else
  BOLD=''; DIM=''; RESET=''
  RED=''; GREEN=''; YELLOW=''; BLUE=''; MAGENTA=''; CYAN=''; GREY=''
fi

SPINNER=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')

print_banner() {
  echo ""
  printf '%s\n' \
"${BLUE}     _       _       ____                   ${RESET}" \
"${BLUE}    / \\   __| | ___ / ___| _   _ _ __   ___ ${RESET}" \
"${BLUE}   / _ \\ / _\` |/ _ \\\\___ \\| | | | '_ \\ / __|${RESET}" \
"${BLUE}  / ___ \\ (_| | (_) |___) | |_| | | | | (__ ${RESET}" \
"${BLUE} /_/   \\_\\__,_|\\___/|____/ \\__, |_| |_|\\___|${RESET}" \
"${BLUE}                          |___/            ${RESET}"
  echo ""
  printf "  ${BOLD}%s${RESET}%s ${DIM}→ %s${RESET}\n\n" "$ORG" "${ONLY_PROJECT:+ / $ONLY_PROJECT}" "$(realpath "$PWD")"
}

# Same reporting as github.sh: one tab-separated line per repo, so line count == progress.
report() {
  local result=$1 reason=$2 color=$3 symbol=$4 name=$5 note=$6 n
  printf '%s\t%s\t%s\n' "$result" "$name" "$reason" >> "$RESULTS_FILE"
  n=$(wc -l < "$RESULTS_FILE" 2>/dev/null); n=${n//[[:space:]]/}
  if [ -n "$note" ]; then
    printf "  ${GREY}[%*d/%d]${RESET} ${color}%s${RESET} %s ${DIM}%s${RESET}\n" \
      "$COUNT_WIDTH" "$n" "$TOTAL_REPOS" "$symbol" "$name" "$note"
  else
    printf "  ${GREY}[%*d/%d]${RESET} ${color}%s${RESET} %s\n" \
      "$COUNT_WIDTH" "$n" "$TOTAL_REPOS" "$symbol" "$name"
  fi
}

# Repositories to ignore, as <project>/<repo>
declare -a IGNORE_LIST=(
)

should_ignore() {
  local key=$1
  for ignored in "${IGNORE_LIST[@]}"; do
    [[ "$key" == "$ignored" ]] && return 0
  done
  return 1
}

# ── Auth ────────────────────────────────────────────────────────────────
# A PAT from the environment wins; otherwise ask Git Credential Manager for the token it stored
# when you signed in. GCM may hand back a PAT (Basic auth) or an Entra access token (Bearer).
AUTH_HEADER=""
if [ -n "$AZURE_DEVOPS_EXT_PAT" ]; then
  AUTH_HEADER="Authorization: Basic $(printf ':%s' "$AZURE_DEVOPS_EXT_PAT" | base64 | tr -d '\n')"
else
  TOKEN=$(printf 'protocol=https\nhost=dev.azure.com\npath=%s\n\n' "$ORG" \
    | GCM_INTERACTIVE=never git credential fill 2> /dev/null | sed -n 's/^password=//p')
  if [ -z "$TOKEN" ]; then
    echo "Error: no Azure DevOps credential found."
    echo "  Sign in once by cloning any repo over HTTPS (Git Credential Manager will prompt),"
    echo "  or export AZURE_DEVOPS_EXT_PAT with a Code (Read) personal access token."
    exit 1
  fi
  if [[ "$TOKEN" == eyJ* ]]; then
    AUTH_HEADER="Authorization: Bearer $TOKEN"
  else
    AUTH_HEADER="Authorization: Basic $(printf ':%s' "$TOKEN" | base64 | tr -d '\n')"
  fi
fi

api() {
  curl -s -H "$AUTH_HEADER" -H "Accept: application/json" "$ADO_URL/$1"
}

# jq on Windows ends lines with \r; strip it so project names, repo names and branches stay clean.
jqr() {
  jq "$@" | tr -d '\r'
}

mkdir -p "$ORG_DIR"
cd "$ORG_DIR" || { echo "Failed to change directory to $ORG_DIR"; exit 1; }

print_banner

# ── Process one repository ───────────────────────────────────────────────
# Args: <project> <repo> <default branch, e.g. master> <clone url>
process_repository() {
  local PROJECT=$1 REPO=$2 BRANCH=$3 URL=$4
  local NAME="$PROJECT/$REPO" DIR="$PROJECT/$REPO"

  if [ -d "$DIR/.git" ]; then
    cd "$DIR" || return 1

    local dirty=false
    if ! git diff --quiet 2> /dev/null || ! git diff --cached --quiet 2> /dev/null; then
      dirty=true
    fi

    # Empty repos (no default branch, or one with no commits on the server): nothing to pull.
    if [ -z "$BRANCH" ] || [ -z "$(git ls-remote --heads origin "$BRANCH" 2> /dev/null)" ]; then
      cd - > /dev/null
      report pull "" "$GREEN" "✓" "$NAME" "empty"
      return 0
    fi

    git checkout "$BRANCH" --quiet 2> /dev/null
    local output pull_status
    output=$(git pull --quiet 2>&1)
    pull_status=$?
    cd - > /dev/null

    if [ $pull_status -ne 0 ]; then
      local reason="unknown"
      if [ "$dirty" = true ]; then
        reason="dirty"
      elif echo "$output" | grep -q "unrelated histories"; then
        reason="diverged"
      elif echo "$output" | grep -q "no such ref"; then
        reason="missing-ref"
      fi
      [ "$VERBOSE" = true ] && echo "$output" | sed 's/^/      /'
      report fail "$reason" "$RED" "✗" "$NAME" "$reason"
      return 1
    fi

    report pull "" "$GREEN" "✓" "$NAME" ""
  else
    mkdir -p "$PROJECT"
    local output clone_status
    output=$(git clone --quiet "$URL" "$DIR" 2>&1)
    clone_status=$?

    if [ $clone_status -ne 0 ]; then
      [ "$VERBOSE" = true ] && echo "$output" | sed 's/^/      /'
      report fail "clone" "$RED" "✗" "$NAME" "clone failed"
      return 1
    fi

    local note="cloned"
    [ -z "$BRANCH" ] && note="cloned (empty)"
    report clone "" "$CYAN" "+" "$NAME" "$note"
  fi

  return 0
}

wait_for_slot() {
  while [ "$(jobs -r | wc -l)" -ge "$MAX_PARALLEL_JOBS" ]; do
    wait -n 2> /dev/null || sleep 0.1
  done
}

rm -f "$RESULTS_FILE"

# ── Phase 1: discover every project and repository up front ──────────────
PROJECTS_JSON=$(api "_apis/projects?api-version=$API_VERSION&\$top=500")
if ! echo "$PROJECTS_JSON" | jq -e '.value | type == "array"' > /dev/null 2>&1; then
  printf "\r\033[K  ${RED}✗ Could not list projects for %s${RESET}\n" "$ORG" >&2
  MESSAGE=$(echo "$PROJECTS_JSON" | jqr -r '.message // empty' 2> /dev/null)
  printf "    ${DIM}%s${RESET}\n\n" "${MESSAGE:-Check the organization name and your sign-in (try: git clone any repo over HTTPS).}" >&2
  exit 1
fi

declare -a PROJECTS=()
while IFS= read -r p; do
  [ -z "$p" ] && continue
  if [ -z "$ONLY_PROJECT" ] || [ "$p" == "$ONLY_PROJECT" ]; then
    PROJECTS+=("$p")
  fi
done <<< "$(echo "$PROJECTS_JSON" | jqr -r '.value[].name' | sort)"

if [ ${#PROJECTS[@]} -eq 0 ]; then
  echo "  ${RED}✗ No project named '$ONLY_PROJECT' in $ORG.${RESET}"
  exit 1
fi

# One queue entry per repo: project \t repo \t default branch \t clone url
declare -a REPO_QUEUE=()
spin_i=0
FOUND=0
for PROJECT in "${PROJECTS[@]}"; do
  ENC_PROJECT=$(jqr -rn --arg p "$PROJECT" '$p|@uri')
  REPOS_JSON=$(api "$ENC_PROJECT/_apis/git/repositories?api-version=$API_VERSION")
  while IFS=$'\t' read -r name disabled branch; do
    [ -z "$name" ] && continue
    FOUND=$((FOUND + 1))
    spin_i=$(( (spin_i + 1) % ${#SPINNER[@]} ))
    printf "\r  ${CYAN}%s${RESET} Discovering repositories… ${DIM}(%d found)${RESET}" "${SPINNER[$spin_i]}" "$FOUND"
    if [ "$disabled" == "true" ]; then
      DISABLED_REPOS=$((DISABLED_REPOS + 1))
      continue
    fi
    if should_ignore "$PROJECT/$name"; then
      SKIPPED_REPOS=$((SKIPPED_REPOS + 1))
      continue
    fi
    url="$ADO_URL/$ENC_PROJECT/_git/$(jqr -rn --arg r "$name" '$r|@uri')"
    REPO_QUEUE+=("$PROJECT"$'\t'"$name"$'\t'"${branch#refs/heads/}"$'\t'"$url")
  done <<< "$(echo "$REPOS_JSON" | jqr -r '.value[] | [.name, (.isDisabled // false | tostring), (.defaultBranch // "")] | @tsv')"
done

TOTAL_REPOS=${#REPO_QUEUE[@]}
COUNT_WIDTH=${#TOTAL_REPOS}

printf "\r\033[K"
printf "  ${BOLD}%d${RESET} repositories in ${BOLD}%d${RESET} project(s)  ${DIM}·${RESET}  ${GREEN}%d to sync${RESET}  ${DIM}·${RESET}  ${YELLOW}%d ignored${RESET}  ${DIM}·${RESET}  ${GREY}%d disabled${RESET}\n\n" \
  "$FOUND" "${#PROJECTS[@]}" "$TOTAL_REPOS" "$SKIPPED_REPOS" "$DISABLED_REPOS"

if [ "$TOTAL_REPOS" -eq 0 ]; then
  echo "  ${DIM}Nothing to sync.${RESET}"
  rm -f "$RESULTS_FILE"
  exit 0
fi

# ── Phase 2: sync (bounded concurrency, live [n/total] progress) ─────────
for ENTRY in "${REPO_QUEUE[@]}"; do
  IFS=$'\t' read -r P R B U <<< "$ENTRY"
  wait_for_slot
  process_repository "$P" "$R" "$B" "$U" &
done

wait

declare -a DIRTY_REPOS OTHER_FAILURES
if [ -f "$RESULTS_FILE" ]; then
  while IFS=$'\t' read -r result name reason; do
    case "$result" in
      pull)  SYNCED_REPOS=$((SYNCED_REPOS + 1)) ;;
      clone) CLONED_REPOS=$((CLONED_REPOS + 1)) ;;
      fail)
        FAILED_REPOS+=("$name")
        if [[ "$reason" == "dirty" ]]; then
          DIRTY_REPOS+=("$name")
        else
          OTHER_FAILURES+=("$name ($reason)")
        fi
        ;;
    esac
  done < "$RESULTS_FILE"
  rm -f "$RESULTS_FILE"
fi

echo ""
printf "  ${DIM}%s${RESET}\n" "────────────────────────────────────────────"
printf "  ${GREEN}✓ %d synced${RESET}   ${CYAN}+ %d cloned${RESET}   ${RED}✗ %d failed${RESET}   ${YELLOW}⊘ %d ignored${RESET}   ${GREY}⊘ %d disabled${RESET}\n" \
  "$SYNCED_REPOS" "$CLONED_REPOS" "${#FAILED_REPOS[@]}" "$SKIPPED_REPOS" "$DISABLED_REPOS"

if [ ${#OTHER_FAILURES[@]} -gt 0 ]; then
  echo ""
  printf "  ${BOLD}${RED}Failed${RESET} ${DIM}(manual fix needed; rerun with -v for details)${RESET}\n"
  for failed in "${OTHER_FAILURES[@]}"; do
    printf "    ${RED}✗${RESET} %s\n" "$failed"
  done
fi

if [ ${#DIRTY_REPOS[@]} -gt 0 ]; then
  echo ""
  printf "  ${BOLD}${YELLOW}Failed${RESET} ${DIM}(local changes)${RESET}\n"
  for repo in "${DIRTY_REPOS[@]}"; do
    printf "    ${YELLOW}✗${RESET} %s\n" "$repo"
  done
  echo ""
  read -r -p "  ${BOLD}Reset these ${#DIRTY_REPOS[@]} repo(s) with git reset --hard and retry? [y/N]${RESET} " response
  if [[ "$response" =~ ^[Yy]$ ]]; then
    echo ""
    for repo in "${DIRTY_REPOS[@]}"; do
      ( cd "$repo" || exit
        git reset --hard HEAD --quiet 2> /dev/null
        branch=$(git symbolic-ref --short refs/remotes/origin/HEAD 2> /dev/null | sed 's#^origin/##')
        [ -n "$branch" ] && git checkout "$branch" --quiet 2> /dev/null
        if git pull --quiet 2> /dev/null; then
          printf "    ${GREEN}✓${RESET} %s ${DIM}(reset + pulled)${RESET}\n" "$repo"
        else
          printf "    ${RED}✗${RESET} %s ${DIM}(still failing after reset)${RESET}\n" "$repo"
        fi )
    done
  fi
fi

if [ ${#FAILED_REPOS[@]} -eq 0 ]; then
  echo ""
  printf "  ${GREEN}✓ All repos up to date.${RESET}\n"
fi
