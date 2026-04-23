#Requires -Version 7
[CmdletBinding()]
param(
    [string]$Config = 'config.yaml'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

Write-Host "Building..."
go build -o localrouter.exe ./cmd/localrouter
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Running with config: $Config"
& ./localrouter.exe -config $Config
