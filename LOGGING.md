# Logs you can pipe

One JSON line per chat request, written where you choose. Off by default.

```bash
ACCESS_LOG=stdout    # or stderr, or a file path like /var/log/gateway/access.jsonl
```

stdout carries only these lines, so a pipe is clean. The normal text log stays on stderr.

## A line

```json
{"ts":"2026-11-02T02:03:26+05:30","request_id":"5f3a91c0d2e4","key":"key_a6b934fb","model":"groq/openai/gpt-oss-120b","provider":"groq","status":200,"result":"ok","ttfb_ms":210,"total_ms":376}
{"ts":"2026-11-02T02:03:27+05:30","request_id":"62a8e4b4d7a0","key":"key_a6b934fb","model":"groq/openai/gpt-oss-120b","status":403,"result":"blocked","filter":"tokens","rule":"ignore-instructions","score":0.99,"ttfb_ms":0,"total_ms":0}
```

| Field | Meaning |
|---|---|
| `ts` | When the request arrived (RFC 3339) |
| `request_id` | Same id the client gets in `X-Gateway-Request-Id` and in a 403 body. Use it to join a user's complaint to a line |
| `key` | `master`, or the virtual key id (not the secret) |
| `model` | What the client asked for, cut at 200 characters |
| `provider` | The provider that answered or failed last. Missing if the request never reached one |
| `status` | HTTP status the client received |
| `result` | `ok`, `provider_error`, `blocked`, `rate_limited`, `model_denied`, `over_budget`, `too_large`, `bad_request` |
| `flagged` | `true` when the filter scored it medium and let it through |
| `filter`, `rule`, `score` | What the filter saw. For you only: clients never get the rule or score |
| `ttfb_ms`, `total_ms` | Time to the provider's first byte, and total time |
| `in_tokens`, `out_tokens`, `cost_usd` | Tokens the provider reported, and what they cost by your price file ([SPEND.md](SPEND.md)). Missing when the provider reported none, and `cost_usd` is missing when the model has no price |

**Never in a line:** the prompt, the answer, any API key, the client's address. Keep it that way if you add fields.

One line is one request, even for a fallback chain. Per-provider tries are in the database (`/v1/stats`).

## Pipe it somewhere

```bash
# Read it live
./gateway | jq -c 'select(.result != "ok")'

# Ship from a file with Vector (vector.toml)
[sources.gw]
type = "file"
include = ["/var/log/gateway/access.jsonl"]
[transforms.parse]
type = "remap"
inputs = ["gw"]
source = '. = parse_json!(.message)'
# then add a sink: loki, elasticsearch, datadog, s3 ...

# Or send stdout straight to Loki with promtail/Alloy, or to journald under systemd
```

Fluent Bit, Promtail, Filebeat and anything else that reads JSON lines works the same way.

## Limits to know

- **Lines are queued and written by another goroutine**, so a stuck pipe never slows a request. If the queue (4096 lines) fills, lines are dropped. A drop count is not exposed yet.
- **A crash can lose the last few lines.** Treat it as a log, not an audit trail.
- **The file is appended and never rotated.** Use logrotate with `copytruncate`, or pipe stdout to something that rotates.
- **Same events are in SQLite** (kept `STATS_DAYS` days, default 30) and shown on the admin page under Requests.
