# Gateway comparison: same fake provider, same load

Run 2026-11-02. One machine (Windows 10, WSL2 Ubuntu, 16 cores, ~10 GB). Everything native Linux inside WSL: the fake provider, the load client and each gateway, over loopback, one gateway at a time. Not a lab benchmark. Vendor numbers were not reproduced.

## Setup
- **Fake provider:** answers instantly. Plain: one 20-word JSON reply. Stream: 20 SSE chunks then `[DONE]`.
- **Request:** about 1 KB prompt, `max_tokens` 64, keep-alive connections, 200 warm-up requests, then 15 s per cell. Zero errors in every cell.
- **Versions:** ours v0.1.0 (signed release build), Bifrost v2.4.0, Helicone ai-gateway 0.2.0-beta.30.
- **Settings:** each as installed, with auth off so the test could run. Ours: filter on, stats on, in-flight cap raised to 4096. Bifrost: default request logging on (a second run with it off is below). Helicone: no Helicone features, telemetry level error.

## Concurrency 1 (no queueing): median latency in microseconds, and added over direct

| | Plain | added | Stream | added |
|---|---|---|---|---|
| direct to provider | 104 | | 228 | |
| **ours, filter on** | 307 | **+203** | 435 | **+207** |
| ours, filter off | 269 | +165 | 403 | +175 |
| Bifrost, default | 957 | +853 | 1444 | +1216 |
| Bifrost, logging off | 794 | +690 | 1177 | +949 |
| Helicone | 626 | +522 | 43974 | +43746 |

## Concurrency 64: requests per second (direct plain = 33,023, stream = 18,985)

| | Plain | Stream |
|---|---|---|
| ours, filter on | 8,623 | 6,981 |
| ours, filter off | 8,960 | 7,279 |
| Bifrost, default | 5,003 | 2,540 |
| Bifrost, logging off | 6,640 | 3,458 |
| Helicone | 6,352 | 1,437 |

## Read this before quoting it
- **The filter costs about 30-40 µs** on a 1 KB prompt (307 vs 269 µs, 435 vs 403 µs).
- **Helicone streaming: about 44 ms per reply in every run.** I did not find the cause. My guess is a 40 ms delayed-acknowledgement stall on small writes over loopback. It may not appear on a real network or with chunks spread out in time. Do not quote it without checking.
- **Bifrost's claimed 11 µs was not reproduced.** I measured about 0.7-0.85 ms added. Its claim may count only its own code at a different load, or use a different configuration. I did not tune Bifrost beyond turning logging off.
- **Disk:** after the runs Bifrost's log database was 456 MB (default logging) and its config database 35 MB. Ours was 17 MB. Bifrost also made an outbound call to `getbifrost.ai` at startup (an MCP library sync).
- **Download size:** Bifrost binary 145 MB, Helicone 23 MB, ours 11.7 MB.
- **Bifrost needs configuration:** out of the box it answered 401 ("virtual key is required") until `enforce_auth_on_inference` was set to false in `config.json`.
- **My own bugs along the way:** the fake provider first chose streaming from a custom header that Bifrost and Helicone do not forward, which made their first streaming numbers invalid. Fixed before the numbers above.
- **Single machine, a load client sharing the CPU, one run per cell, no repeats.** Differences under about 15% mean nothing.
- **Concurrency 15 run** (an earlier pass): ours 6,420 plain / 5,315 stream rps, Bifrost 4,128 / 2,250, Helicone 5,255 / 339. Same ordering.
