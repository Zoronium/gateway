# Using Claude Code through the gateway

The gateway answers the Anthropic Messages format at `/v1/messages`, translates it to chat completions, runs
the same filter, key limits and fallback chain, and translates the answer back. Text, images, tools, tool
results and streaming work. Thinking blocks, citations and prompt-cache hints are dropped.

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_AUTH_TOKEN=<GATEWAY_KEY or a virtual key>
export ANTHROPIC_MODEL=groq/openai/gpt-oss-120b
export ANTHROPIC_DEFAULT_SONNET_MODEL=$ANTHROPIC_MODEL
export ANTHROPIC_DEFAULT_HAIKU_MODEL=$ANTHROPIC_MODEL   # Claude Code makes small background calls too
claude
```

- Set every model variable to a `provider/model` the gateway knows, or Claude Code sends a Claude name and the
  gateway answers 400.
- Claude Code sends 15,000+ tokens of instructions with every request. Free tiers with small per-minute
  limits (Groq's free plan, for one) answer 413. Use a provider and model with a large window.
- If you are signed in to Claude Code, it may send its own login instead of your token. Run it with a clean
  `CLAUDE_CONFIG_DIR` to be sure.
- `/v1/messages/count_tokens` returns a rough guess (four bytes a token).

Tested: real Groq answers, streamed tool calls and tool-result follow-ups through `/v1/messages`
(`anthropic_test.go` covers the translation). Not tested: a full Claude Code session, because the free keys
on hand could not take its prompt size.
