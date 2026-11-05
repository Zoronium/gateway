# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Existing codebase: Go server, one embedded static file (`admin.html`) with inline style and script, served with a Content Security Policy that allows only that file's own style and script by hash. No framework, no build step, no third-party scripts, fonts or images at runtime. Dependency count is part of the product's promise.

## Users

A solo developer or a small team (one to three people) running their own AI gateway. They open the admin page occasionally to check error rates and blocked requests, to give each app its own key with limits, and to find out why a request failed. Confirmed: not a wall display, not a large team with roles.

## Product Purpose

The gateway is a small, secure, OpenAI-compatible proxy in front of several model providers, with fallback, a prompt-injection filter and error-rate stats. The admin page is where an operator sees what the gateway did and controls who may use it. Success: from a cold open the operator knows within seconds whether anything is wrong, and can find the one request that went wrong.

## Positioning

A gateway whose operator can read all of it: few dependencies, signed releases, nothing hidden, and an admin page that shows only what the gateway actually did. LiteLLM-class tools are heavy and dial-filled; this one works with defaults and shows real data.

## Operating Context

Runs on the operator's own machine or server. The page is served by the gateway itself at `/admin` and signs in with the master gateway key (kept in the tab's session storage). Data comes from the gateway's own SQLite events and stats. Logs also go to a JSON access log.

## Capabilities and Constraints

Real and available to show: request events (time, key, model, provider, status, result, filter name and rule and score, time to first byte, total time); per-provider tries, error rate and average first-byte time; model lists per provider; virtual keys (name, allowed models, requests per minute, created, revoked); filter thresholds and runtime settings (read only).
Not available, so never shown as if real: dollar spend or budgets, tokens, regions or clusters, circuit breakers, caching, PII scrubbing, canary tokens, HSM or KMS, SOC2 or compliance claims, users or roles, audit trail of admin actions, editing settings from the page.
Terminology: "virtual key" for a per-app key, "master key" for GATEWAY_KEY, "filter" for the prompt-injection filter, results are `ok`, `provider_error`, `blocked`, `rate_limited`, `model_denied`, `too_large`, `bad_request`.
Constraint: nothing may be loaded from another origin.

## Brand Commitments

None beyond the repository's name ("gateway"). The mockup's invented product name is not adopted.

## Evidence on Hand

Real data only. Source design reference: the Stitch export "stitch_aegis_ai_control_plane" (screens and the Obsidian Telemetry design system), supplied by the user as the target look. Its sample numbers are not evidence and must not appear.

## Product Principles

1. Show only what the gateway did. An empty panel is better than an invented number.
2. The one thing to notice comes first: a problem is visible without scrolling or clicking.
3. Every number leads to the request behind it.
4. Operator secrets are handled with care: a key is shown once, nothing sensitive is stored in the page.
5. No dependency is added for looks.

## Accessibility & Inclusion

Operator tool, keyboard reachable, readable contrast in both light and dark, motion reduced when the system asks for it.
