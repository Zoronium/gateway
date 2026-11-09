# gateway

A small AI gateway in Go. One OpenAI-compatible endpoint in front of several providers, with fallback, a prompt-injection filter and error-rate stats. One binary, 10 pinned dependencies, signed releases.

**Status: early (v0.1.0).** Built by one person. Read [RESULTS.md](RESULTS.md) before trusting the filter: it caught 43% of attacks on a fresh test set, and the misses are listed. A speed comparison with Bifrost and Helicone, caveats included, is in [COMPARE.md](COMPARE.md).

## Run it

1. Download the binary for your system and the three signing files from the [latest release](../../releases/latest), then verify them ([RELEASE.md](RELEASE.md)).
2. Copy `.env.example` to `.env` and paste at least one provider key.
3. Start it. It prints a gateway key (`gw-…`) if you did not set `GATEWAY_KEY`.
4. Point any OpenAI client at `http://127.0.0.1:8080/v1` with that key. Choose the provider in the model name: `groq/openai/gpt-oss-120b`. A comma-separated list is a fallback chain.

Or build it yourself: `go build -trimpath .` (Go 1.27.3, pinned in `go.mod`).

## What it does

- **Providers:** openai, anthropic (compatibility endpoint), openrouter, groq, nvidia, mistral, huggingface, ollama.
- **Streaming:** server-sent events pass straight through.
- **Filter:** a heuristic tripwire, not protection. It scores new request text and tool results, blocks high scores with a 403 and flags medium ones. It misses most paraphrases and only reads input. Details and limits in [RESULTS.md](RESULTS.md).
- **Safety defaults:** bearer key required, Host allow-list, JSON only, request and scan limits, server timeouts.
- **Stats:** `GET /v1/models`, `/v1/providers`, `/v1/stats` with error rates, stored in SQLite ([API.md](API.md)).
- **Admin page:** `/admin` (sign in with `GATEWAY_KEY`): traffic chart with blocked requests marked, error rates and timing against the previous period, a searchable request log with a per-request inspector, what the filter caught by rule, virtual keys with allowed models and requests-per-minute limits, providers and models, read-only settings. Dark, with a light theme that follows your system.
- **Spend and budgets:** tokens and cost per request from a price file you supply, and a monthly dollar budget per virtual key. Treat budgets as a guard rail, not a ledger: requests are held at their worst case before they are sent, but prices are yours to keep right and spend is per process ([SPEND.md](SPEND.md)).
- **Logs for pipelines:** `ACCESS_LOG=stdout` writes one JSON line per request, never prompts or keys ([LOGGING.md](LOGGING.md)).
- **Roadmap:** what is next and what waits is in [ROADMAP.md](ROADMAP.md).

## What it does not do

No plugins, no response scanning, no PII masking. The admin page is a small MVP (one shared sign-in key, read-only settings). The filter is a first filter, not full protection. Claude Code connects through `/v1/messages` (see [CLAUDE_CODE.md](CLAUDE_CODE.md)), and Codex CLI through `/v1/responses` (see [CODEX.md](CODEX.md)). Everything else that is missing is in [ROADMAP.md](ROADMAP.md).

## Security

Report problems privately through GitHub security advisories on this repo.

## License

MIT, see [LICENSE](LICENSE).
