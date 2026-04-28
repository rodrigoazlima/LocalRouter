#!/usr/bin/env bash
set -euo pipefail

# ─── Environment Variables ────────────────────────────────────────────────────
# Set only the keys for providers you want to enable.
# Providers with an unset or empty key are silently skipped at startup.
#
# export OPENROUTER_API_KEY=   # OpenRouter    — 500+ free :free models (20 RPM, 200 RPD)
# export GROQ_API_KEY=         # Groq          — Llama 3/4, Qwen3, DeepSeek-R1 (30 RPM)
# export NVIDIA_API_KEY=       # NVIDIA NIM    — free credits (~40 RPM)
# export GITHUB_TOKEN=         # GitHub Models — GPT-4.1, o4-mini, Llama 4 (10-15 RPM)
# export GOOGLE_API_KEY=       # Google Gemini — Flash / Flash-lite (15-30 RPM)
# export COHERE_API_KEY=       # Cohere        — Command R/A (20 RPM, 1K/month)
# export MISTRAL_API_KEY=      # Mistral AI    — Small, Nemo, Codestral (free tier)
# export ZHIPU_API_KEY=        # Zhipu Z AI    — GLM-4.x-Flash (1 concurrent)
# export CEREBRAS_API_KEY=     # Cerebras      — Llama 3.3/Qwen3, ultra-fast (30 RPM)
# export SILICONFLOW_API_KEY=  # SiliconFlow   — Qwen3, DeepSeek-R1/V3 (1000 RPM)
# (llm7-1 needs no key — 30 RPM free)
# export ANTHROPIC_KEY=        # Anthropic     — paid
# export OPENAI_KEY=           # OpenAI        — paid
# export DEEPSEEK_KEY=         # DeepSeek      — paid
# export VLLM_KEY=             # vLLM local    — only if auth enabled
# ─────────────────────────────────────────────────────────────────────────────

CONFIG="${1:-config.yaml}"

echo "Building..."
go build -o localrouter ./cmd/localrouter

echo "Running with config: $CONFIG"
./localrouter -config "$CONFIG"
