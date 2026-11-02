# Roadmap

What is built, what comes next, and what waits. Sizes are rough: S is a day or less, M a few days, L a week or more.

## Built (v0.1.x, in the repo now)

| Area | What you get |
|---|---|
| Proxy | One OpenAI-compatible `/v1/chat/completions`, streaming, 8 providers, fallback chains |
| Safety | Bearer key, Host allow-list, JSON only, limits, timeouts, prompt-injection filter (static tokens) |
| Keys | Virtual keys: name, allowed models (`groq/*`), requests per minute. Secret shown once, hash stored |
| Admin page | `/admin`: overview with error rates, keys (create, revoke), request log, read-only settings |
| Logs | `ACCESS_LOG` JSON lines for any log pipeline ([LOGGING.md](LOGGING.md)); same events in SQLite |
| Stats API | `/v1/models`, `/v1/providers`, `/v1/stats` with error rates |
| Release | Signed binaries, checksums, dependency list, pinned toolchain |

## Next (v0.2): what blocks real use

| Item | Why | Size |
|---|---|---|
| **Responses API (`/v1/responses`) and Anthropic Messages (`/v1/messages`)** | Found in testing: Codex CLI dropped chat completions, and Claude Code speaks the Anthropic format. Without these, the two biggest agents cannot connect. Pi works today | L |
| Longer wait for local models | First call to a cold Ollama model times out at 60 s | S |
| Scan tool-call arguments and responses (review #5) | The filter only reads what goes in. An injection shows up as a tool call going out | L |
| Spend and budgets per key | The feature teams ask for first. Needs a price table per model | M |
| Key expiry and per-key token-per-minute limit | Natural extensions of what exists | S |
| Edit settings from the page (filter thresholds, limits) | Today these need `.env` and a restart. Needs a safe write path and a restart-free reload | M |
| Prometheus `/metrics` | Lets existing dashboards work with no new UI | S |

## Later

| Item | Notes | Size |
|---|---|---|
| Provider keys managed in the page | Needs encryption at rest and a plan for the master secret. Do not store them in clear text | M |
| Routing rules in the page: aliases, fallback chains, weights | Today the client writes the chain in the `model` field | M |
| Teams and users, roles | Admin vs viewer. Needed before more than one person administers | L |
| SSO (OIDC) for the admin page | After roles exist | M |
| Audit log of admin actions | Who created or revoked which key | S |
| Alerts and webhooks | Error-rate spike, block spike | M |
| Postgres backend | Several gateway copies sharing keys and limits. Rate limits are per process today | L |
| AI-based filter (stage 6) | Runs only when the cheap filter is unsure. Needs its own test set | L |
| PII masking, response caching | Both were scoped out of v1 | L |
| Optional prompt and answer storage with retention | Off by default. Some teams need it, many must not | M |
| CSV export from the page | | S |

## Known limits of what is built

- **Admin sign-in is one shared key** (`GATEWAY_KEY`). Anyone with it can create and revoke keys. There is no audit trail yet.
- **The master key lives in the browser tab's session storage** while you are signed in. A browser extension that reads page storage could see it. The page's script is locked down with a Content Security Policy, but treat the admin page like any admin tool: use a clean browser.
- **Rate limits reset on restart** and are not shared between copies.
- **No spend tracking.** Limits are requests per minute only.
- **Settings are read-only in the page.**
- **A virtual key sees only its own models** in `/v1/models`, and cannot read stats or the admin API.
