#Requires -Version 7
#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Install LocalRouter as a Windows Service.

.DESCRIPTION
    Builds localrouter.exe, installs it to $InstallDir, registers it as a
    Windows service via NSSM, and starts it. Requires administrator rights.

.PARAMETER Action
    install   Build, copy, register and start the service (default)
    uninstall Stop and remove the service, leave files intact
    start     Start the service
    stop      Stop the service
    status    Show service status

.PARAMETER InstallDir
    Root install directory. Defaults to $HOME\.localllmrouter

.PARAMETER ServiceName
    Windows service name. Defaults to LocalRouter

.PARAMETER Port
    HTTP listen port. Defaults to 8080

.PARAMETER SkipBuild
    Skip go build step (use existing binary in project root)

.EXAMPLE
    .\scripts\install.ps1
    .\scripts\install.ps1 -Action uninstall
    .\scripts\install.ps1 -Port 9000 -SkipBuild
#>

[CmdletBinding(SupportsShouldProcess)]
param(
    [ValidateSet('install', 'uninstall', 'start', 'stop', 'status')]
    [string]$Action = 'install',

    [string]$InstallDir = "$env:USERPROFILE\.localllmrouter",
    [string]$ServiceName = 'LocalRouter',
    [int]$Port = 8080,
    [switch]$SkipBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ── Paths ─────────────────────────────────────────────────────────────────────

$ProjectRoot  = (Resolve-Path "$PSScriptRoot\..").Path
$BinDir       = Join-Path $InstallDir 'bin'
$LogDir       = Join-Path $InstallDir 'logs'
$ConfigDir    = Join-Path $InstallDir 'config'
$BinaryName   = 'localrouter.exe'
$BinaryDest   = Join-Path $BinDir $BinaryName
$ConfigDest   = Join-Path $ConfigDir 'config.yaml'
$NssmPath     = Join-Path $InstallDir 'bin\nssm.exe'
$NssmUrl      = 'https://nssm.cc/release/nssm-2.24.zip'
$NssmVersion  = 'nssm-2.24'

# ── Helpers ───────────────────────────────────────────────────────────────────

function Write-Step([string]$msg) {
    Write-Host "  >> $msg" -ForegroundColor Cyan
}

function Write-OK([string]$msg) {
    Write-Host "  OK $msg" -ForegroundColor Green
}

function Write-Fail([string]$msg) {
    Write-Host "FAIL $msg" -ForegroundColor Red
    exit 1
}

function Require-Command([string]$name) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        Write-Fail "'$name' not found in PATH. Install it and retry."
    }
}

function Get-Nssm {
    if (Test-Path $NssmPath) { return $NssmPath }

    # Try system-installed nssm first (chocolatey, scoop, etc.)
    $sys = Get-Command nssm -ErrorAction SilentlyContinue
    if ($sys) { return $sys.Source }

    Write-Step "NSSM not found — downloading $NssmVersion"
    $zip  = Join-Path $env:TEMP 'nssm.zip'
    $tmp  = Join-Path $env:TEMP $NssmVersion
    Invoke-WebRequest -Uri $NssmUrl -OutFile $zip -UseBasicParsing
    Expand-Archive -Path $zip -DestinationPath $env:TEMP -Force
    $extracted = Join-Path $tmp 'win64\nssm.exe'
    if (-not (Test-Path $extracted)) { Write-Fail "NSSM extraction failed." }
    Copy-Item $extracted $NssmPath -Force
    Remove-Item $zip, $tmp -Recurse -Force -ErrorAction SilentlyContinue
    Write-OK "NSSM copied to $NssmPath"
    return $NssmPath
}

# ── Actions ───────────────────────────────────────────────────────────────────

function Invoke-Status {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) {
        Write-Host "Service '$ServiceName' is not installed." -ForegroundColor Yellow
        return
    }
    Write-Host "Service : $($svc.DisplayName)" -ForegroundColor Cyan
    Write-Host "Status  : $($svc.Status)"      -ForegroundColor Cyan
    Write-Host "Start   : $($svc.StartType)"   -ForegroundColor Cyan
}

function Invoke-Start {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) { Write-Fail "Service '$ServiceName' not installed. Run install first." }
    Start-Service -Name $ServiceName
    Write-OK "Service started."
}

function Invoke-Stop {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) { Write-Host "Service not installed." -ForegroundColor Yellow; return }
    Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
    Write-OK "Service stopped."
}

function Invoke-Uninstall {
    $nssm = Get-Nssm

    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) {
        Write-Host "Service '$ServiceName' is not installed — nothing to remove." -ForegroundColor Yellow
        return
    }

    Write-Step "Stopping service"
    & $nssm stop $ServiceName confirm 2>$null | Out-Null

    Write-Step "Removing service"
    & $nssm remove $ServiceName confirm
    if ($LASTEXITCODE -ne 0) { Write-Fail "NSSM failed to remove service." }

    Write-OK "Service '$ServiceName' removed. Files remain at: $InstallDir"
}

function Invoke-Install {
    # ── 1. Prerequisites ──────────────────────────────────────────────────────
    Write-Host "`nLocalRouter Windows Service Installer" -ForegroundColor White
    Write-Host "======================================" -ForegroundColor White

    if (-not $SkipBuild) { Require-Command 'go' }

    # ── 2. Build ──────────────────────────────────────────────────────────────
    if (-not $SkipBuild) {
        Write-Step "Building localrouter.exe"
        Push-Location $ProjectRoot
        try {
            $commit  = git rev-parse --short HEAD 2>$null
            $version = git describe --tags --always 2>$null
            $ldflags = "-s -w -X main.version=$version -X main.commit=$commit"
            go build -trimpath -ldflags $ldflags -o "$ProjectRoot\localrouter.exe" ./cmd/localrouter
            if ($LASTEXITCODE -ne 0) { Write-Fail "go build failed." }
        } finally { Pop-Location }
        Write-OK "Build complete"
    }

    $BinarySource = Join-Path $ProjectRoot 'localrouter.exe'
    if (-not (Test-Path $BinarySource)) { Write-Fail "Binary not found: $BinarySource" }

    # ── 3. Directory layout ───────────────────────────────────────────────────
    Write-Step "Creating directory structure at $InstallDir"
    foreach ($d in @($BinDir, $LogDir, $ConfigDir)) {
        New-Item -ItemType Directory -Path $d -Force | Out-Null
    }
    Write-OK "Directories ready"

    # ── 4. Copy files ─────────────────────────────────────────────────────────
    $svcRunning = (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)?.Status -eq 'Running'
    if ($svcRunning) {
        Write-Step "Stopping running service to replace binary"
        Stop-Service -Name $ServiceName -Force
        Write-OK "Service stopped"
    }

    Write-Step "Copying binary"
    Copy-Item $BinarySource $BinaryDest -Force
    Write-OK "Binary -> $BinaryDest"

    $srcConfig = Join-Path $ProjectRoot 'config.yaml'
    if (-not (Test-Path $ConfigDest)) {
        if (Test-Path $srcConfig) {
            Write-Step "Copying config.yaml (first-time install)"
            Copy-Item $srcConfig $ConfigDest -Force
            Write-OK "Config  -> $ConfigDest"
        } else {
            Write-Host "  WARN config.yaml not found in project root — create $ConfigDest manually." -ForegroundColor Yellow
        }
    } else {
        Write-OK "Config already exists at $ConfigDest (not overwritten)"
    }

    # ── 5. Get NSSM ───────────────────────────────────────────────────────────
    Write-Step "Locating NSSM"
    $nssm = Get-Nssm
    Write-OK "NSSM: $nssm"

    # ── 6. Remove stale service if exists ─────────────────────────────────────
    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Step "Removing existing '$ServiceName' service"
        & $nssm stop   $ServiceName confirm 2>$null | Out-Null
        & $nssm remove $ServiceName confirm
    }

    # ── 7. Register service ───────────────────────────────────────────────────
    Write-Step "Registering Windows service '$ServiceName'"
    & $nssm install $ServiceName $BinaryDest
    if ($LASTEXITCODE -ne 0) { Write-Fail "NSSM install failed." }

    # Arguments passed to the binary
    & $nssm set $ServiceName AppParameters "-config `"$ConfigDest`" -port $Port"

    # Working directory
    & $nssm set $ServiceName AppDirectory $InstallDir

    # Display name & description
    & $nssm set $ServiceName DisplayName  'LocalRouter LLM Proxy'
    & $nssm set $ServiceName Description  'OpenAI-compatible LLM routing proxy (github.com/rodrigoazlima/localrouter)'

    # Start type: automatic (delayed) — starts after network is ready
    & $nssm set $ServiceName Start SERVICE_DELAYED_AUTO_START

    # Stdout / stderr logs (rotated at 10 MB)
    & $nssm set $ServiceName AppStdout          (Join-Path $LogDir 'localrouter.log')
    & $nssm set $ServiceName AppStderr          (Join-Path $LogDir 'localrouter-error.log')
    & $nssm set $ServiceName AppRotateFiles     1
    & $nssm set $ServiceName AppRotateOnline    1
    & $nssm set $ServiceName AppRotateBytes     10485760   # 10 MB

    # Restart policy: restart on crash, throttle after repeated failures
    & $nssm set $ServiceName AppExit            Default Restart
    & $nssm set $ServiceName AppRestartDelay    3000       # 3 s initial delay
    & $nssm set $ServiceName AppThrottle        30000      # back-off after 30 s of repeated crashes

    # Service depends on network being up
    & $nssm set $ServiceName DependOnService    Tcpip

    # ── 8. Environment variables (system env is inherited; service env block) ─
    #    Read current user env vars for API keys and pass them to the service.
    $envKeys = @(
        'OPENROUTER_API_KEY', 'GROQ_API_KEY', 'NVIDIA_API_KEY', 'GITHUB_TOKEN',
        'MISTRAL_API_KEY', 'GOOGLE_API_KEY', 'ANTHROPIC_KEY', 'DEEPSEEK_KEY',
        'COHERE_API_KEY', 'ZHIPU_API_KEY', 'CEREBRAS_API_KEY',
        'SILICONFLOW_API_KEY', 'OLLAMA_API_KEY', 'KILO_API_KEY'
    )
    $envPairs = @()
    foreach ($key in $envKeys) {
        $val = [System.Environment]::GetEnvironmentVariable($key, 'User')
        if (-not $val) {
            $val = [System.Environment]::GetEnvironmentVariable($key, 'Machine')
        }
        if ($val) { $envPairs += "$key=$val" }
    }
    if ($envPairs.Count -gt 0) {
        # NSSM AppEnvironmentExtra preserves inherited env and adds/overrides keys
        & $nssm set $ServiceName AppEnvironmentExtra ($envPairs -join "`n")
        Write-OK "Injected $($envPairs.Count) API key(s) into service environment"
    } else {
        Write-Host "  WARN No API key env vars found in user/machine scope." -ForegroundColor Yellow
        Write-Host "       Set them in System Properties -> Environment Variables," -ForegroundColor Yellow
        Write-Host "       then reinstall or edit $ConfigDest directly." -ForegroundColor Yellow
    }

    Write-OK "Service registered"

    # ── 9. Start ──────────────────────────────────────────────────────────────
    Write-Step "Starting service"
    & $nssm start $ServiceName
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  WARN Service registered but failed to start immediately." -ForegroundColor Yellow
        Write-Host "       Check logs: $LogDir" -ForegroundColor Yellow
    } else {
        Start-Sleep -Milliseconds 1500
        $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        Write-OK "Service status: $($svc.Status)"
    }

    # ── Summary ───────────────────────────────────────────────────────────────
    Write-Host ""
    Write-Host "Installation complete!" -ForegroundColor Green
    Write-Host "  Service  : $ServiceName"
    Write-Host "  Endpoint : http://localhost:$Port/v1"
    Write-Host "  Config   : $ConfigDest"
    Write-Host "  Logs     : $LogDir"
    Write-Host ""
    Write-Host "Useful commands:" -ForegroundColor White
    Write-Host "  Start   : Start-Service $ServiceName"
    Write-Host "  Stop    : Stop-Service  $ServiceName"
    Write-Host "  Status  : .\scripts\install.ps1 -Action status"
    Write-Host "  Logs    : Get-Content '$LogDir\localrouter.log' -Tail 50 -Wait"
    Write-Host "  Upgrade : .\scripts\install.ps1 -SkipBuild:$false"
}

# ── Dispatch ──────────────────────────────────────────────────────────────────

switch ($Action) {
    'install'   { Invoke-Install }
    'uninstall' { Invoke-Uninstall }
    'start'     { Invoke-Start }
    'stop'      { Invoke-Stop }
    'status'    { Invoke-Status }
}
