# Spend and budgets

The gateway counts the tokens each answer used and, if you give it prices, what they cost. A virtual key can
have a monthly dollar budget. When it is used up, that key gets a 403 until the 1st of the next month (UTC) or
until you raise the budget.

## Set up

1. Write a price file: US dollars per million tokens, by `provider/model`. A trailing `*` matches a prefix, and
   an exact entry wins over a prefix. See [prices.example.json](prices.example.json).
2. Start the gateway with `PRICES_FILE=prices.json`.
3. Create a key with a budget on the admin page (Keys), or: `POST /admin/api/keys` with `{"name":"app","budget_usd":25}`.

**The gateway ships no prices.** They change, and a stale one is worse than none. Copy them from your
provider's pricing page and check them now and then. The example file is a shape, not a source.

## What you get

- `in_tokens`, `out_tokens` and `cost_usd` on each request in the admin page, the database and the access log.
- Spend on the overview (when a price file is loaded) and spend against budget per key on the Keys page.
- `over_budget` as a request result, counted under "Refused".

## Rules worth knowing

- **A key with a budget may only use priced models.** An unpriced model is refused (`model_unpriced`), so a
  missing price can never be a way around the budget. Give free and local models a price of 0.
- **Streaming:** the gateway always sets `stream_options.include_usage` on a streamed chat request, whatever the
  client sent, so the provider reports tokens in a final chunk. Clients that follow the OpenAI format ignore the extra chunk.
- **A provider that reports no tokens is charged for its input only**, estimated at four bytes a token. The same goes for a client that hangs up before the final usage chunk. Output is not estimated, so check a new provider with a few requests.
- **A budget can be overshot** by what the requests already running cost when it is reached.
- **Per process.** Spend is read back from the database at start, but two gateway copies do not share it.
- Only virtual keys have budgets. The master key is never limited.
