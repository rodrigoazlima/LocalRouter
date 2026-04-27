#Requires -Version 7
[CmdletBinding()]
param(
    [string]$Config = 'config.yaml'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ─── Environment Variables ────────────────────────────────────────────────────
# Set only the keys for providers you want to enable.
# Providers with an unset or empty key are silently skipped at startup.
#
# $env:OPENROUTER_KEY = ''  # OpenRouter — 500+ free :free models
# $env:GROQ_KEY       = ''  # Groq       — Llama 3.x, Kimi K2 (free tier)
# $env:NVIDIA_API_KEY     = ''  # NVIDIA NIM — free credits
# $env:GITHUB_TOKEN   = ''  # GitHub Models — GPT-4o, Llama (free tier)
# $env:GOOGLE_API_KEY     = ''  # Google Gemini — Flash / Flash-lite (free tier)
# $env:COHERE_KEY     = ''  # Cohere     — Command R / R+ (free tier)
# $env:MISTRAL_KEY    = ''  # Mistral AI — dev quota (free tier)
# $env:ZHIPU_KEY      = ''  # Zhipu AI   — GLM-4-Flash (free tier)
# $env:ANTHROPIC_KEY  = ''  # Anthropic  — paid
# $env:OPENAI_KEY     = ''  # OpenAI     — paid
# $env:DEEPSEEK_KEY   = ''  # DeepSeek   — paid
# $env:VLLM_KEY       = ''  # vLLM local — only if auth enabled
# ─────────────────────────────────────────────────────────────────────────────

Write-Host "Building..."
go build -o localrouter.exe ./cmd/localrouter
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Running with config: $Config"
& ./localrouter.exe -config $Config
