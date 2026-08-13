#!/bin/bash
# Write release notes for a commit range to stdout.
#
#   ./changelog.sh <git-range>
#   ./changelog.sh v2.0.0..HEAD
#
# One bullet per commit, merges left out. Used by the release workflows to fill
# in the body of a GitHub release; run it by hand to preview what the next
# release would say.
set -euo pipefail

RANGE="${1:?usage: changelog.sh <git-range>}"

if [[ -z "$(git log --no-merges "$RANGE")" ]]; then
  echo "No changes."
  exit 0
fi

git log --no-merges --reverse --pretty=format:'- %s (%h)' "$RANGE"
echo
