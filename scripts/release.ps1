#Requires -Version 7
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Version
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($Version -notmatch '^v\d+\.\d+\.\d+$') {
    Write-Error "Version must match v<major>.<minor>.<patch> (got: $Version)"
    exit 1
}

$dirty = git status --porcelain
if ($dirty) {
    Write-Error "Uncommitted changes present. Commit or stash before releasing."
    exit 1
}

$branch = git rev-parse --abbrev-ref HEAD
if ($branch -ne 'master') {
    Write-Error "Must be on master branch (current: $branch)"
    exit 1
}

Write-Host "Running tests..."
go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Tagging $Version..."
git tag -a $Version -m "Release $Version"

Write-Host "Pushing tag..."
git push origin $Version

Write-Host "Done. GitHub Actions will publish the release."
