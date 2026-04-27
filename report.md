# Provider Error Report — 2026-04-27

## Providers Working Successfully

### lmstudio-local
**Status:** ✅ Working  
**Details:** Probe OK (7ms), discovered 19 models including `qwen3-coder-next`, `gemma-3-27b-it-qat`, `glm-4.6v-flash`  
**Note:** This provider successfully handled the request `[7b739e4b]` with response about config updates

### openrouter-1
**Status:** ✅ Probe OK, ⚠️ Rate Limited During Requests  
**Details:** 
- Probe OK (140ms), discovered 360 models
- Request failed with HTTP 429: `qwen/qwen3-coder:free is temporarily rate-limited upstream`
- Suggestion: Add your own key to accumulate rate limits at https://openrouter.ai/settings/integrations

### google-1
**Status:** ✅ Probe OK, ⚠️ Transient Errors  
**Details:**
- Probe OK (301ms)
- Request returned empty stream (model started response but returned nothing)
- Router correctly fell through to next provider (lmstudio-local)

### cohere-1
**Status:** ✅ Probe OK, ❌ Deprecated Model  
**Details:**
- Probe OK (351ms)
- Request failed with HTTP 404: `model 'command-r' was removed on September 15, 2025`
**Fix:** Update config to current models:
```yaml
models:
  - id: command-r7b-12-2024
    is_free: true
  - id: command-a-03-2025
    is_free: true
```

### github-models-1
**Status:** ✅ Probe OK, ⚠️ Rate Limited During Requests  
**Details:**
- Probe OK (494ms), discovered 43 models including `openai/gpt-5`, `gpt-4o`, `meta-llama/llama-4-maverick`
- Request failed with HTTP 429: `Too many requests. For more on scraping GitHub and how it may affect your rights...`
**Fix:** Reduce request limits (try 10/min) or increase recovery_window

---

## Providers Failing Health Checks

### mistral-1
**Error:** `probe failed: health check: HTTP 404` / `discover models failed: list models: HTTP 404`  
**Cause:** Mistral API returns 404 on health/models probe path at `/v1/models`  
**Fix:** Verify `MISTRAL_API_KEY` is set and valid. Check router probe path matches Mistral's endpoint

### groq-1
**Error:** `probe failed: health check: HTTP 404` / `discover models failed: list models: HTTP 404`  
**Cause:** Router sends health probe to `/models` path on Groq API, but this returns 404  
**Fix:** Verify `GROQ_API_KEY` is set. Check if Groq supports the model list endpoint - may need custom health_path

### nvidia-1
**Error:** `probe failed: health check: HTTP 404` / `discover models failed: list models: HTTP 404`  
**Cause:** `/v1/models` returns 404. `api_key` is set at model level only, not provider level  
**Fix:** Move `api_key: ${NVIDIA_API_KEY}` to provider level (not just model level)

### kilo-1
**Error:** `probe failed: health check: HTTP 405` / `discover models failed: list models: HTTP 405`  
**Cause:** `/api/gateway` returns 405 Method Not Allowed - probe sends GET but endpoint expects POST  
**Fix:** Add `health_path` config pointing to a valid GET endpoint for kilo.ai, or disable health probe

### lmstudio-local-2
**Error:** `probe failed: Get "http://192.168.2.124:1234/v1/models": context deadline exceeded`  
**Cause:** Connection timeout to remote LM Studio instance at 192.168.2.124 (likely unreachable)  
**Fix:** Verify IP address is correct and the LM Studio instance is running on port 1234

---

## Providers Not in Current Config

The following providers were in previous reports but are now commented out in config.yaml:

| Provider | Previous Error | Current Status |
|---|---|---|
| zhipu-1 | api_key resolves empty | Commented out (lines 136-147) |
| ollama-local | Connection refused | Commented out (lines 149-158) |
| vllm-local | Connection refused | Commented out (lines 179-188) |

---

## Request [7b739e4b] Routing Summary

**Request:** `model="auto" stream=true`  
**Routing Path:**
1. **openrouter-1** → Failed: HTTP 429 (rate limited)
2. **github-models-1** → Failed: HTTP 429 (too many requests)
3. **google-1** → Empty stream
4. **cohere-1** → Failed: HTTP 404 (deprecated model 'command-r')
5. **lmstudio-local** → ✅ Success

**Response:** Fix for config.yaml - update OpenRouter endpoint and reorganize local provider configuration

---

## Summary Table

| Provider | State | Priority Action |
|---|---|---|
| lmstudio-local | ✅ Available | None |
| openrouter-1 | ⚠️ Rate Limited | Check key or increase window |
| google-1 | ⚠️ Transient errors | No action (auto-retry works) |
| cohere-1 | ❌ Broken config | Update deprecated model IDs |
| github-models-1 | ⚠️ Rate limited | Reduce request limits |
| mistral-1 | ❌ Probe failed | Verify API key and endpoint path |
| groq-1 | ❌ Probe failed | Verify API key and health path |
| nvidia-1 | ❌ Probe failed | Move api_key to provider level |
| kilo-1 | ❌ Probe failed (405) | Fix probe method or add health_path |
| lmstudio-local-2 | ❌ Unreachable | Verify remote LM Studio is accessible |

---

*Report generated from logs at 2026-04-27 09:38*