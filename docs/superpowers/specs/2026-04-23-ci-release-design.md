# CI & Release Workflow Design

Date: 2026-04-23
Project: LocalRouter (`github.com/rodrigoazlima/localrouter`)

## Overview

Two GitHub Actions workflows plus a GoReleaser config and local release helper scripts. CI verifies every push/PR including cross-compilation snapshot; releases are triggered by pushing a `v*` tag.

## Files

```
.github/workflows/ci.yml
.github/workflows/release.yml
.goreleaser.yaml
scripts/release.sh
scripts/release.ps1
```

## CI Workflow (`ci.yml`)

**Triggers:** `push` to master, `pull_request` (any branch)

**Steps:**
1. `actions/checkout`
2. `actions/setup-go@v5` — Go 1.22, module cache enabled
3. `go vet ./...`
4. `go test -race -count=1 ./...`
5. `goreleaser check` — validates `.goreleaser.yaml`
6. `goreleaser build --snapshot --clean` — cross-compiles all 5 targets, verifies no platform breaks

## Release Workflow (`release.yml`)

**Triggers:** `push: tags: ['v*']`

**Permissions:** `contents: write` (to create GitHub release)

**Steps:**
1. `actions/checkout` with `fetch-depth: 0` (GoReleaser needs full tag history for changelog)
2. `actions/setup-go@v5` — Go 1.22
3. `goreleaser release --clean` with `GITHUB_TOKEN`

## GoReleaser Config (`.goreleaser.yaml`)

**Targets (5):**
- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`
- `windows/amd64`

**Build flags:** `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"`

**Archives:**
- `.tar.gz` for linux and darwin
- `.zip` for windows

**Checksum:** `sha256sums.txt`

**Changelog:** grouped by `feat`, `fix`, `chore` from conventional commit prefixes

## Release Scripts

**`scripts/release.sh`** (bash) and **`scripts/release.ps1`** (PowerShell) — identical logic:

1. Accept `VERSION` arg, validate `v\d+\.\d+\.\d+` format
2. Abort if uncommitted changes present (`git status --porcelain`)
3. Confirm current branch is master
4. Run `go test ./...` locally
5. `git tag -a $VERSION -m "Release $VERSION"`
6. `git push origin $VERSION` → triggers `release.yml`

## Constraints

- No Docker image push (binaries-only release)
- No commits made by automation — developer runs `scripts/release.sh` or `scripts/release.ps1` to cut releases
- `.github` is not in `.gitignore` — safe to commit
