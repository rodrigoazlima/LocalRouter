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
# $env:OPENROUTER_API_KEY  = ''  # OpenRouter    — 500+ free :free models (20 RPM, 200 RPD)
# $env:GROQ_API_KEY        = ''  # Groq          — Llama 3/4, Qwen3, DeepSeek-R1 (30 RPM)
# $env:NVIDIA_API_KEY      = ''  # NVIDIA NIM    — free credits (~40 RPM)
# $env:GITHUB_TOKEN        = ''  # GitHub Models — GPT-4.1, o4-mini, Llama 4 (10-15 RPM)
# $env:GOOGLE_API_KEY      = ''  # Google Gemini — Flash / Flash-lite (15-30 RPM)
# $env:COHERE_API_KEY      = ''  # Cohere        — Command R/A (20 RPM, 1K/month)
# $env:MISTRAL_API_KEY     = ''  # Mistral AI    — Small, Nemo, Codestral (free tier)
# $env:ZHIPU_API_KEY       = ''  # Zhipu Z AI    — GLM-4.x-Flash (1 concurrent)
# $env:CEREBRAS_API_KEY    = ''  # Cerebras      — Llama 3.3/Qwen3, ultra-fast (30 RPM)
# $env:SILICONFLOW_API_KEY = ''  # SiliconFlow   — Qwen3, DeepSeek-R1/V3 (1000 RPM)
# $env:OLLAMA_API_KEY      = ''  # Ollama Cloud  — 400+ models, gpt-oss:120b (session limits)
# (llm7-1 needs no key — 30 RPM free)
# $env:ANTHROPIC_KEY       = ''  # Anthropic     — paid
# $env:OPENAI_KEY          = ''  # OpenAI        — paid
# $env:DEEPSEEK_KEY        = ''  # DeepSeek      — paid
# ─────────────────────────────────────────────────────────────────────────────

Write-Host "Building..."
go build -o localrouter.exe ./cmd/localrouter
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Running with config: $Config"
& ./localrouter.exe -config $Config
