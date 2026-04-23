#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-}"

if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 <version>  (e.g. v1.2.3)" >&2
  exit 1
fi

if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Error: version must match v<major>.<minor>.<patch> (got: $VERSION)" >&2
  exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
  echo "Error: uncommitted changes present. Commit or stash before releasing." >&2
  exit 1
fi

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$BRANCH" != "master" ]]; then
  echo "Error: must be on master branch (current: $BRANCH)" >&2
  exit 1
fi

echo "Running tests..."
go test ./...

echo "Tagging $VERSION..."
git tag -a "$VERSION" -m "Release $VERSION"

echo "Pushing tag..."
git push origin "$VERSION"

echo "Done. GitHub Actions will publish the release."
