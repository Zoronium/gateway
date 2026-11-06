# API: models, providers and error rates

All three endpoints need the gateway key (`Authorization: Bearer <GATEWAY_KEY>`) and answer JSON.

| Endpoint | What it returns |
|---|---|
| `GET /v1/models` | Every model each provider lists, in the OpenAI list shape, with its error rate |
| `GET /v1/providers` | One row per provider: model count, last refresh, error rate |
| `GET /v1/stats` | Counts by outcome for each provider and model |

**Options (all three):** `?since=24h` (also `30m`, `7d`; default 24h, at most 366d) and `?provider=groq`.
**Only on `/v1/models`:** `?refresh=1` fetches every provider's list again before answering.

## What counts as an error

Each try at a provider is one row. A fallback chain writes one row per provider it tried. The gateway's own refusals (bad key, filter block, scan limit) are not provider tries and are not counted.

| Class | Meaning | Error? |
|---|---|---|
| `ok` | 2xx answer | no |
| `rate_limited` | HTTP 429 | yes |
| `server_error` | HTTP 5xx | yes |
| `unreachable` | no answer: connection failed or timed out | yes |
| `client_error` | other 4xx, such as a bad key (401), a missing model (404) or a retired model (410) | yes |
| `stream_error` | the provider answered 200 and then dropped the stream | yes |

`error_rate` = errors / requests, rounded to 4 places. `avg_ttfb_ms` is the average time until the provider's response headers arrived, over successful tries only (`null` if there were none).

## Example: `GET /v1/models?provider=nvidia&since=1h` (shortened)

```json
{
  "object": "list",
  "since": "1h0m0s",
  "data": [
    {
      "id": "nvidia/openai/gpt-oss-120b", "object": "model", "created": 1793517604,
      "owned_by": "nvidia", "provider": "nvidia", "model": "openai/gpt-oss-120b",
      "listed": false,
      "stats": {
        "requests": 1, "errors": 1, "error_rate": 1, "avg_ttfb_ms": null,
        "by_class": { "ok": 0, "rate_limited": 0, "server_error": 0, "unreachable": 0, "client_error": 1, "stream_error": 0 }
      }
    }
  ]
}
```

- `id` is exactly what you put in the `model` field of a chat request.
- `listed: false` means the model was called but the provider's own list does not include it, such as a retired model. This is how a dead model shows up.
- `created` is the time of the last model-list refresh, not the model's release date.
- `stats` is left out when a model had no tries in the window.

## Where the model lists come from

Each provider's own `GET <base>/models`, fetched at startup and then every `MODELS_REFRESH_MIN` minutes (default 60). A failed refresh keeps the old list and shows `refresh_ok: false` with a short `refresh_error` in `/v1/providers`.

- The lists include every kind of model the provider has, such as speech and image models, not only chat models.
- Anthropic's list is requested with its own headers. I have not tested it.

## What is stored

SQLite file `gateway.db` (change with `DB_PATH`). Per try: time, provider, model name, HTTP status, outcome class, time to first byte. Plus the model lists and refresh results. **Never** prompts, answers, keys or client details. Rows older than `STATS_DAYS` (default 30) are deleted.

## Limits

- Writes are queued and saved in batches, so the last second of stats can be lost if the process dies. If the queue is ever full, tries are dropped and counted in `dropped` on `/v1/stats`.
- One database connection, so reads and writes take turns.
- If the database cannot be opened, the proxy keeps running and these endpoints answer `503`.
- A client can send any model name, so names are cut to 200 characters. A caller with the key could still fill the table with made-up names; the 30-day clean-up bounds it.

## Admin API (master key only)

Used by the admin page at `/admin`. A virtual key gets 403 on all of these, and on `/v1/providers` and `/v1/stats`.

| Endpoint | What |
|---|---|
| `GET /admin/api/summary?since=24h` | This period and the one before it (requests, blocked, flagged, refused, provider errors), first-byte p50 and p95 for answered requests, counts by result, by key (with last use) and by filter rule |
| `GET /admin/api/series?since=24h&buckets=60` | Requests in equal time buckets (total, ok, blocked, flagged, limited, errors), for the traffic chart |
| `GET /admin/api/events?since=24h&limit=100` | Recent requests, newest first. Optional: `result=blocked` (or a list such as `rate_limited,model_denied`), `key=<id>`, `q=<text>` (request id, model, key id or key name), `threat=1` (blocked or flagged), `attention=1` (anything an operator should look at: not ok and not a bad request, or flagged) |
| `GET /admin/api/keys` | Virtual keys (never the secret) |
| `POST /admin/api/keys` | `{"name":"my app","models":["groq/*"],"rpm":60}`. The secret is in the answer once |
| `POST /admin/api/keys/{id}/revoke` | Stops a key at once |
| `GET /admin/api/config` | How the gateway is set up. Read only, no secrets |

Results: `ok`, `provider_error`, `blocked`, `rate_limited`, `model_denied`, `too_large`, `bad_request`.
