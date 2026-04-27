#Requires -Version 7
# This script tests avalible models and build quick report
[CmdletBinding()]
param(
    [string]$RouterUrl = 'http://localhost:8080',
    [string]$LogFile   = "$PSScriptRoot\..\localrouter.err"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$pass = 0
$fail = 0

# Track model availability
$availableModels = @{}
$unavailableModels = @{}

function Write-Pass([string]$msg) { Write-Host "  [PASS] $msg" -ForegroundColor Green; $script:pass++ }
function Write-Fail([string]$msg) { Write-Host "  [FAIL] $msg" -ForegroundColor Red;  $script:fail++ }
function Write-Info([string]$msg) { Write-Host "  [INFO] $msg" -ForegroundColor Cyan }

function Get-LogTail {
    param([int]$Lines = 20)
    if (Test-Path $LogFile) {
        Get-Content $LogFile | Select-Object -Last $Lines
    }
}

function Invoke-RouterPost {
    param([string]$Model, [bool]$Stream = $false)
    $body = @{
        model    = $Model
        messages = @(@{ role = 'user'; content = 'What is 2+2? Reply with just the number.' })
        stream   = $Stream
    } | ConvertTo-Json -Compress
    try {
        $response = Invoke-RestMethod `
            -Uri "$RouterUrl/v1/chat/completions" `
            -Method POST `
            -Headers @{ 'Content-Type' = 'application/json' } `
            -Body $body `
            -TimeoutSec 30
        return @{ Ok = $true; Data = $response; Raw = $null }
    } catch {
        $raw = $null
        try { $raw = $_.ErrorDetails.Message | ConvertFrom-Json } catch {}
        return @{ Ok = $false; Data = $null; Raw = $raw; Status = $_.Exception.Response.StatusCode }
    }
}

function Assert-LogContains {
    param([string]$Pattern, [string]$Description)
    $tail = Get-LogTail -Lines 30
    if ($tail | Select-String -Pattern $Pattern -Quiet) {
        Write-Pass "Log contains: $Description"
    } else {
        Write-Fail "Log missing:  $Description"
        Write-Info "Log tail:"
        $tail | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
    }
}

# ─── Mark log position before test ───────────────────────────────────────────
$logBefore = if (Test-Path $LogFile) { (Get-Content $LogFile).Count } else { 0 }

function Get-NewLogLines {
    if (Test-Path $LogFile) {
        $all = Get-Content $LogFile
        if ($all.Count -gt $logBefore) { $all[$logBefore..($all.Count - 1)] } else { @() }
    } else { @() }
}

function Assert-NewLogContains {
    param([string]$Pattern, [string]$Description)
    $new = Get-NewLogLines
    if ($new | Select-String -Pattern $Pattern -Quiet) {
        Write-Pass "Log contains: $Description"
    } else {
        Write-Fail "Log missing:  $Description"
        Write-Info "New log lines:"
        $new | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
    }
}

# ─── Test 1: Health check ─────────────────────────────────────────────────────
Write-Host "`n[TEST 1] Router health" -ForegroundColor Yellow
try {
    $health = Invoke-RestMethod -Uri "$RouterUrl/health" -TimeoutSec 5
    if ($health.local.status -in 'healthy', 'degraded') {
        Write-Pass "Router is up (local.status=$($health.local.status))"
    } else {
        Write-Fail "Router status unexpected: $($health.local.status)"
    }
    $githubNode = $health.local.nodes | Where-Object { $_.id -eq 'github-models-1' }
    if ($githubNode -and $githubNode.status -eq 'ready') {
        Write-Pass "github-models-1 node is ready"
    } else {
        Write-Fail "github-models-1 not ready (status=$($githubNode?.status))"
    }
} catch {
    Write-Fail "Health endpoint unreachable: $_"
}

# ─── Test 2: openai/gpt-5 returns expected response (unavailable) ─────────────
Write-Host "`n[TEST 2] openai/gpt-5 — expect 400 (model unavailable via API)" -ForegroundColor Yellow
$script:logBefore = if (Test-Path $LogFile) { (Get-Content $LogFile).Count } else { 0 }

$result = Invoke-RouterPost -Model 'openai/gpt-5'

if (-not $result.Ok) {
    Write-Pass "Router returned error response (expected — gpt-5 is preview-only)"
    $errMsg = $result.Raw?.error?.message
    Write-Info "Error: $errMsg"
    # Track as unavailable model
    $unavailableModels['openai/gpt-5'] = "HTTP $($result.Status) - $errMsg"
} else {
    Write-Fail "Expected error but got success: $($result.Data | ConvertTo-Json -Compress)"
}

Start-Sleep -Milliseconds 200
Assert-NewLogContains -Pattern 'IN from=.*model="openai/gpt-5"' `
    -Description 'Router received openai/gpt-5 request'
Assert-NewLogContains -Pattern 'github-models-1 failed' `
    -Description 'GitHub API was called and returned error'

# ─── Test 3: gpt-4o (working model) non-streaming ─────────────────────────────
Write-Host "`n[TEST 3] gpt-4o — non-streaming (should succeed)" -ForegroundColor Yellow
$script:logBefore = if (Test-Path $LogFile) { (Get-Content $LogFile).Count } else { 0 }

$result = Invoke-RouterPost -Model 'gpt-4o'

if ($result.Ok) {
    $answer = $result.Data.choices[0].message.content
    $model  = $result.Data.model
    Write-Pass "Response received: model=$model answer=`"$answer`""
    # Track gpt-4o as available (non-streaming)
    $availableModels['gpt-4o'] = "OK"
    if ($result.Data.usage.completion_tokens -gt 0) {
        Write-Pass "Usage tokens present (prompt=$($result.Data.usage.prompt_tokens) completion=$($result.Data.usage.completion_tokens))"
    } else {
        Write-Fail "No usage tokens in response"
    }
} else {
    $unavailableModels['gpt-4o'] = "HTTP $($result.Status)"
    Write-Fail "Request failed: $($result.Raw | ConvertTo-Json -Compress)"
}

Start-Sleep -Milliseconds 200
Assert-NewLogContains -Pattern '→ github-models-1 model="gpt-4o' `
    -Description 'Log confirms GitHub API was called for gpt-4o'

# ─── Test 5: cohere-1 health check ─────────────────────────────────────────────
Write-Host "`n[TEST 5] cohere-1 provider health" -ForegroundColor Yellow
$cohereNode = $health.local.nodes | Where-Object { $_.id -eq 'cohere-1' }
if ($cohereNode) {
    if ($cohereNode.status -in 'ready', 'healthy') {
        Write-Pass "cohere-1 provider is ready (status=$($cohereNode.status))"
    } else {
        Write-Fail "cohere-1 not ready (status=$($cohereNode.status))"
    }
} else {
    Write-Fail "cohere-1 provider not found in health response"
}

# ─── Test 6: command-a-03-2025 non-streaming ───────────────────────────────────
Write-Host "`n[TEST 6] command-a-03-2025 — non-streaming (should succeed)" -ForegroundColor Yellow
$script:logBefore = if (Test-Path $LogFile) { (Get-Content $LogFile).Count } else { 0 }

$result = Invoke-RouterPost -Model 'command-a-03-2025'

if ($result.Ok) {
    $answer = $result.Data.choices[0].message.content
    $model  = $result.Data.model
    Write-Pass "Response received: model=$model answer=`"$answer`""
    $availableModels['cohere/command-a-03-2025'] = "OK"
    if ($result.Data.usage.completion_tokens -gt 0) {
        Write-Pass "Usage tokens present (prompt=$($result.Data.usage.prompt_tokens) completion=$($result.Data.usage.completion_tokens))"
    } else {
        Write-Fail "No usage tokens in response"
    }
} else {
    $errMsg = $result.Raw?.error?.message
    $unavailableModels['cohere/command-a-03-2025'] = "HTTP $($result.Status) - $errMsg"
    Write-Fail "Request failed: HTTP $($result.Status)"
    Write-Info "Error: $errMsg"
}

Start-Sleep -Milliseconds 200
Assert-NewLogContains -Pattern '→ cohere-1 model="command-a-03-2025"' `
    -Description 'Log confirms Cohere API was called for command-a-03-2025'

# ─── Test 7: command-r7b-12-2024 streaming ─────────────────────────────────────
Write-Host "`n[TEST 7] command-r7b-12-2024 — streaming (should succeed)" -ForegroundColor Yellow
$script:logBefore = if (Test-Path $LogFile) { (Get-Content $LogFile).Count } else { 0 }

try {
    $streamBody = @{
        model    = 'command-r7b-12-2024'
        messages = @(@{ role = 'user'; content = 'Say hello.' })
        stream   = $true
    } | ConvertTo-Json -Compress

    # curl.exe handles SSE correctly; PowerShell's web clients buffer responses
    $tmpBody = [System.IO.Path]::GetTempFileName()
    Set-Content -Path $tmpBody -Value $streamBody -Encoding UTF8 -NoNewline

    $raw = & curl.exe -s -X POST "$RouterUrl/v1/chat/completions" `
        -H 'Content-Type: application/json' `
        --data-binary "@$tmpBody" `
        --max-time 20
    Remove-Item $tmpBody -ErrorAction SilentlyContinue

    $chunks  = 0
    $content = ''
    foreach ($line in ($raw -split "`n")) {
        $line = $line.Trim()
        if ($line -match '^data: (.+)$') {
            $data = $Matches[1]
            if ($data -eq '[DONE]') { break }
            try {
                $obj   = $data | ConvertFrom-Json
                $delta = $obj.choices[0].delta.content
                if ($delta) { $content += $delta; $chunks++ }
            } catch {}
        }
    }

    if ($chunks -gt 0) {
        Write-Pass "Received $chunks stream chunks: `"$($content.Trim())`""
        $availableModels['cohere/command-r7b-12-2024 (streaming)'] = "OK"
    } else {
        Write-Fail "No stream chunks received (raw length=$($raw.Length))"
        $unavailableModels['cohere/command-r7b-12-2024 (streaming)'] = "No response data"
    }
} catch {
    Write-Fail "Streaming request failed: $_"
}

Start-Sleep -Milliseconds 200
Assert-NewLogContains -Pattern '→ cohere-1 model="command-r7b-12-2024" stream=true' `
    -Description 'Log confirms Cohere API called for command-r7b-12-2024 streaming'

# ─── Test 8: gpt-4o streaming ────────────────────────────────────────────────
Write-Host "`n[TEST 8] gpt-4o — streaming (should succeed)" -ForegroundColor Yellow
$script:logBefore = if (Test-Path $LogFile) { (Get-Content $LogFile).Count } else { 0 }

try {
    $streamBody = @{
        model    = 'gpt-4o'
        messages = @(@{ role = 'user'; content = 'Say hello.' })
        stream   = $true
    } | ConvertTo-Json -Compress

    # curl.exe handles SSE correctly; PowerShell's web clients buffer responses
    $tmpBody = [System.IO.Path]::GetTempFileName()
    Set-Content -Path $tmpBody -Value $streamBody -Encoding UTF8 -NoNewline

    $raw = & curl.exe -s -X POST "$RouterUrl/v1/chat/completions" `
        -H 'Content-Type: application/json' `
        --data-binary "@$tmpBody" `
        --max-time 20
    Remove-Item $tmpBody -ErrorAction SilentlyContinue

    $chunks  = 0
    $content = ''
    foreach ($line in ($raw -split "`n")) {
        $line = $line.Trim()
        if ($line -match '^data: (.+)$') {
            $data = $Matches[1]
            if ($data -eq '[DONE]') { break }
            try {
                $obj   = $data | ConvertFrom-Json
                $delta = $obj.choices[0].delta.content
                if ($delta) { $content += $delta; $chunks++ }
            } catch {}
        }
    }

    if ($chunks -gt 0) {
        Write-Pass "Received $chunks stream chunks: `"$($content.Trim())`""
        # Track gpt-4o streaming as available
        $availableModels['gpt-4o (streaming)'] = "OK"
    } else {
        Write-Fail "No stream chunks received (raw length=$($raw.Length))"
        $unavailableModels['gpt-4o (streaming)'] = "No response data"
    }
} catch {
    Write-Fail "Streaming request failed: $_"
}

Start-Sleep -Milliseconds 200
Assert-NewLogContains -Pattern '→ github-models-1 model="gpt-4o" stream=true' `
    -Description 'Log confirms GitHub API called for gpt-4o streaming'

# ─── Model Availability Report ────────────────────────────────────────────────
Write-Host "`n[MODEL AVAILABILITY REPORT]" -ForegroundColor Cyan

if ($availableModels.Count -gt 0) {
    Write-Host "`nAvailable Models:" -ForegroundColor Green
    foreach ($model in $availableModels.Keys | Sort-Object) {
        Write-Host "  [OK] $model"
    }
}

if ($unavailableModels.Count -gt 0) {
    Write-Host "`nUnavailable Models (not available with current setup/token):" -ForegroundColor Yellow
    foreach ($model in $unavailableModels.Keys | Sort-Object) {
        Write-Host "  [FAIL] $model - $($unavailableModels[$model])"
    }
}

# ─── Summary ──────────────────────────────────────────────────────────────────
Write-Host "`n─────────────────────────────────" -ForegroundColor DarkGray
$total = $pass + $fail
$availCount = $availableModels.Count + $unavailableModels.Count
if ($availCount -gt 0) {
    Write-Host "Tests: $pass/$total passed | Models: $($availableModels.Count) available, $($unavailableModels.Count) unavailable" -ForegroundColor $(if ($fail -eq 0) { 'Green' } else { 'Yellow' })
} else {
    Write-Host "Results: $pass/$total passed" -ForegroundColor $(if ($fail -eq 0) { 'Green' } else { 'Yellow' })
}
if ($fail -gt 0) { exit 1 }