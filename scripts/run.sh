#!/usr/bin/env bash
set -euo pipefail

# ─── Environment Variables ────────────────────────────────────────────────────
# Set only the keys for providers you want to enable.
# Providers with an unset or empty key are silently skipped at startup.
#
# export OPENROUTER_KEY=   # OpenRouter — 500+ free :free models
# export GROQ_KEY=         # Groq       — Llama 3.x, Kimi K2 (free tier)
# export NVIDIA_KEY=       # NVIDIA NIM — free credits
# export GITHUB_TOKEN=     # GitHub Models — GPT-4o, Llama (free tier)
# export GOOGLE_KEY=       # Google Gemini — Flash / Flash-lite (free tier)
# export COHERE_KEY=       # Cohere     — Command R / R+ (free tier)
# export MISTRAL_KEY=      # Mistral AI — dev quota (free tier)
# export ZHIPU_KEY=        # Zhipu AI   — GLM-4-Flash (free tier)
# export ANTHROPIC_KEY=    # Anthropic  — paid
# export OPENAI_KEY=       # OpenAI     — paid
# export DEEPSEEK_KEY=     # DeepSeek   — paid
# export VLLM_KEY=         # vLLM local — only if auth enabled
# ─────────────────────────────────────────────────────────────────────────────

CONFIG="${1:-config.yaml}"

echo "Building..."
go build -o localrouter ./cmd/localrouter

echo "Running with config: $CONFIG"
./localrouter -config "$CONFIG"
