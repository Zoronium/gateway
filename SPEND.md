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
- **A request is held at its worst case before it is sent:** its input (four bytes a token) plus `max_tokens` (or
  `max_completion_tokens`, or `max_output_tokens`) at the model's output price. A request with no limit is assumed
  to be allowed 4096 tokens. If what is already spent, plus what requests still running may cost, plus this
  request would pass the budget, it is refused with 403 `budget_exceeded` before it reaches the provider. So
  requests running at the same time cannot together pass the budget. A budgeted key should send a sensible
  `max_tokens`; a very large one can be refused even when the real answer would be short.
- **When the real cost is known, that is what is charged.** If the provider reports no tokens, the stream was cut
  off, or the client hung up before the final usage chunk, the key is charged the worst case above, so hanging
  up cannot dodge the budget. The tokens shown for such a request are that estimate, not a count.
- **The reserve is only as good as `max_tokens` and your prices.** A provider that generates more than the limit it was given, or a stale price, still costs more than the gateway counts.
- **Per process.** Spend is read back from the database at start, but two gateway copies do not share it.
- Only virtual keys have budgets. The master key is never limited.
