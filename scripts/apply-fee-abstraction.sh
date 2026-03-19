#!/usr/bin/env bash
# apply-fee-abstraction.sh
#
# Cherry-picks a specific set of commits onto the current (or given) branch,
# then squashes them into a single "feat: apply fee abstraction" commit.
#
# Usage:
#   ./scripts/apply-fee-abstraction.sh [OPTIONS]
#
# Options:
#   -b, --branch <branch>    Branch to apply commits onto (default: current branch)
#   -c, --config <file>      Commits config file (default: scripts/fee-abstraction-commits.txt)
#   -m, --message <msg>      Squash commit message (default: see below)
#   -n, --no-squash          Cherry-pick without squashing
#   -s, --source <remote>    Remote/repo to fetch commits from before applying (optional)
#   -h, --help               Show this help
#
# Commits config file format (one commit SHA per line, # for comments):
#   # fee abstraction core
#   09a681f9
#   beedbe82
#   ...

set -euo pipefail

##############################################################################
# Defaults
##############################################################################

COMMITS_FILE="scripts/fee-abstraction-commits.txt"
TARGET_BRANCH=""
SQUASH=true
SOURCE_REMOTE=""
SQUASH_MESSAGE="feat: apply fee abstraction changes

Cherry-picked fee abstraction commits from upstream fork.
See scripts/fee-abstraction-commits.txt for the full list."

##############################################################################
# Helpers
##############################################################################

info()    { echo "[info]  $*"; }
success() { echo "[ok]    $*"; }
warn()    { echo "[warn]  $*"; }
die()     { echo "[error] $*" >&2; exit 1; }

usage() {
  sed -n '/^# Usage/,/^[^#]/{ /^#/{ s/^# \{0,1\}//; p } }' "$0"
  exit 0
}

require_clean_tree() {
  if ! git diff --quiet || ! git diff --cached --quiet; then
    die "Working tree is dirty. Commit or stash your changes first."
  fi
}

read_commits() {
  local file="$1"
  [[ -f "$file" ]] || die "Commits file not found: $file"
  # Strip blank lines and comments
  grep -Ev '^\s*(#|$)' "$file"
}

##############################################################################
# Argument parsing
##############################################################################

while [[ $# -gt 0 ]]; do
  case "$1" in
    -b|--branch)  TARGET_BRANCH="$2"; shift 2 ;;
    -c|--config)  COMMITS_FILE="$2";  shift 2 ;;
    -m|--message) SQUASH_MESSAGE="$2"; shift 2 ;;
    -n|--no-squash) SQUASH=false;     shift   ;;
    -s|--source)  SOURCE_REMOTE="$2"; shift 2 ;;
    -h|--help)    usage ;;
    *) die "Unknown option: $1" ;;
  esac
done

##############################################################################
# Main
##############################################################################

ORIGINAL_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
TARGET_BRANCH="${TARGET_BRANCH:-$ORIGINAL_BRANCH}"

info "Reading commits from: $COMMITS_FILE"
mapfile -t COMMITS < <(read_commits "$COMMITS_FILE")

[[ ${#COMMITS[@]} -gt 0 ]] || die "No commits found in $COMMITS_FILE"

info "Commits to apply (${#COMMITS[@]}):"
for sha in "${COMMITS[@]}"; do
  msg="$(git log --format='%s' -1 "$sha" 2>/dev/null || echo '(not yet fetched)')"
  echo "    $sha  $msg"
done

# Optional: fetch from a source remote so the SHAs are available locally
if [[ -n "$SOURCE_REMOTE" ]]; then
  info "Fetching from remote: $SOURCE_REMOTE"
  git fetch "$SOURCE_REMOTE"
fi

# Verify all SHAs exist
for sha in "${COMMITS[@]}"; do
  git cat-file -e "${sha}^{commit}" 2>/dev/null \
    || die "Commit not found locally: $sha (use -s/--source to fetch first)"
done

require_clean_tree

# Switch to target branch if needed
if [[ "$ORIGINAL_BRANCH" != "$TARGET_BRANCH" ]]; then
  info "Switching to branch: $TARGET_BRANCH"
  git checkout "$TARGET_BRANCH"
fi

APPLY_START_REF="$(git rev-parse HEAD)"
info "Applying on top of: $APPLY_START_REF"

# Cherry-pick each commit
FAILED=()
for sha in "${COMMITS[@]}"; do
  short="$(git log --format='%h %s' -1 "$sha")"
  info "Picking: $short"
  if ! git cherry-pick "$sha"; then
    warn "Cherry-pick failed for $sha — attempting to continue after resolving"
    FAILED+=("$sha")
    # Abort so the user can fix manually
    git cherry-pick --abort 2>/dev/null || true
    # Switch back if we moved branches
    [[ "$ORIGINAL_BRANCH" != "$TARGET_BRANCH" ]] && git checkout "$ORIGINAL_BRANCH"
    die "Cherry-pick failed for $sha. Resolve conflicts and re-run, or remove the commit from $COMMITS_FILE."
  fi
done

success "All ${#COMMITS[@]} commits applied."

##############################################################################
# Squash
##############################################################################

if $SQUASH; then
  APPLIED_COUNT="${#COMMITS[@]}"
  info "Squashing $APPLIED_COUNT commits..."

  # Soft-reset to the base, keep the tree
  git reset --soft "$APPLY_START_REF"

  git commit -m "$SQUASH_MESSAGE"

  SQUASH_SHA="$(git rev-parse --short HEAD)"
  success "Squashed into: $SQUASH_SHA"
else
  success "Done (no squash requested)."
fi

info "Branch '$TARGET_BRANCH' is ready."
