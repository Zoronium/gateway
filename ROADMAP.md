# Roadmap

What is built, what comes next, and what waits. Sizes are rough: S is a day or less, M a few days, L a week or more.

## Built (v0.1.x, in the repo now)

| Area | What you get |
|---|---|
| Proxy | OpenAI-compatible `/v1/chat/completions` and Anthropic-format `/v1/messages` and OpenAI Responses `/v1/responses` (text, images, tools, streaming), 8 providers, fallback chains |
| Safety | Bearer key, Host allow-list, JSON only, limits, timeouts, prompt-injection filter (static tokens) |
| Keys | Virtual keys: name, allowed models (`groq/*`), requests per minute. Secret shown once, hash stored |
| Admin page | `/admin`: overview with a traffic chart (blocked requests as stems), previous-period comparison and first-byte timing; requests with search and filters; a request inspector; threats by rule; keys (create, revoke, last use); providers and models; read-only settings. Dark, with a matching light theme |
| Spend | Tokens and cost per request from your own price file, monthly dollar budget per virtual key, spend on the overview and the keys page ([SPEND.md](SPEND.md)) |
| Logs | `ACCESS_LOG` JSON lines for any log pipeline ([LOGGING.md](LOGGING.md)); same events in SQLite |
| Stats API | `/v1/models`, `/v1/providers`, `/v1/stats` with error rates |
| Release | Signed binaries, checksums, dependency list, pinned toolchain |

## Next (v0.2): what blocks real use

| Item | Why | Size |
|---|---|---|
| Longer wait for local models | First call to a cold Ollama model times out at 60 s | S |
| Scan tool-call arguments and responses (review #5) | The filter only reads what goes in. An injection shows up as a tool call going out | L |
| Key expiry and per-key token-per-minute limit | Natural extensions of what exists | S |
| Edit settings from the page (filter thresholds, limits) | Today these need `.env` and a restart. Needs a safe write path and a restart-free reload | M |
| Prometheus `/metrics` | Lets existing dashboards work with no new UI | S |
| Embed the Geist and JetBrains Mono font files in the admin page | Today it uses them only if installed and falls back to system fonts. Embedding needs a download and adds about 150 KB to the binary | S |
| Time-range picker with custom dates, and per-key charts | The page has 1H, 24H and 7D only | S |

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
- **Spend is only as good as your price file and what providers report.** A provider that sends no token counts costs nothing here. A budget can be overshot by the requests running when it is reached. Budgets reset on the 1st (UTC) and are per process.
- **Settings are read-only in the page.**
- **A virtual key sees only its own models** in `/v1/models`, and cannot read stats or the admin API.
