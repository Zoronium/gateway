# Using Codex CLI through the gateway

The gateway answers the OpenAI Responses format at `/v1/responses`, translates it to chat completions, runs
the same filter, key limits, budgets and fallback chain, and translates the answer back (plain and streamed).
Text, images and function tools work. Reasoning items, web search, local shell and freeform tools are dropped,
and conversation state is whatever the client sends each time (no `previous_response_id`).

Put this in `~/.codex/config.toml` (or a `CODEX_HOME` of your own):

```toml
model = "groq/openai/gpt-oss-120b"   # any provider/model the gateway knows
model_provider = "gw"

[model_providers.gw]
name = "gateway"
base_url = "http://127.0.0.1:8080/v1"
env_key = "GW_KEY"                    # set GW_KEY to your gateway key or a virtual key
wire_api = "responses"
```

- Codex warns that it has no metadata for the model name and uses fallback settings. That is expected.
- Codex sends 8,000+ tokens with each request. Free tiers with small per-minute limits (Groq's free plan allows
  8,000 for `gpt-oss-20b`) answer 413 and Codex retries a few times, then stops. Use a model with a larger window.
- Run `codex exec` with stdin closed (`< /dev/null`) in scripts, or it waits for input.

Tested: Codex CLI 0.164 connected through `/v1/responses` to real Groq, got a streamed tool call back and
ran it. The next request went over Groq's free per-minute cap, so a full multi-step session is not tested.
`responses_test.go` covers the translation, including streamed events and tool calls.
