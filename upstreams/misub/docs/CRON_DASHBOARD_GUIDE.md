# MiSub Cron Dashboard Guide

This guide documents the operator-facing cron management flow currently wired
into the EasyProxy monorepo copy of MiSub.

## Routes

- `GET /cron?secret=<cronSecret>`
  - external scheduler entrypoint
  - uses the `cronSecret` saved in MiSub settings
- `GET /api/cron/status`
  - authenticated operator API
  - returns the latest persisted execution summary when KV is available
- `POST /api/cron/trigger`
  - authenticated operator API
  - manually runs the same cron pipeline from the current browser session
- `GET /cron-dashboard`
  - lightweight operator page
  - calls the two authenticated `/api/cron/*` endpoints

## Authentication Model

`/cron-dashboard`, `/api/cron/status`, and `/api/cron/trigger` rely on the
normal MiSub admin session cookie. If the session is missing or expired, the
dashboard will fail to load cron state until the operator logs in again.

`/cron` is separate:

- it does not use the admin session
- it requires `cronSecret`
- it is intended for uptime monitors or external scheduler services

## Basic Setup

1. Log into MiSub.
2. Open Settings.
3. In the Cron card, set a `cronSecret`.
4. Save settings.
5. Open `/cron-dashboard` to verify the operator page loads.
6. Copy the generated `/cron?secret=...` URL into your external scheduler.

## Recommended Scheduler Pattern

For the current monorepo deployment model, treat `/cron` as the stable machine
entrypoint and keep Pages-native cron configuration optional.

Recommended cadence:

- every 30 minutes to every 24 hours, depending on how often subscriptions
  change

## Reference Pages Config

If you are deploying MiSub manually to Cloudflare Pages without the monorepo
helper scripts, use:

- `upstreams/misub/wrangler-cf-pages.toml`

Notes:

- this file is reference-only
- the repository deploy scripts still use `upstreams/misub/wrangler.jsonc`
- runtime secrets and bindings should be configured in the Cloudflare Pages UI

## Troubleshooting

If `/cron-dashboard` shows an authorization error:

- log into MiSub again
- verify the browser is sending the MiSub session cookie

If `/api/cron/status` returns empty history:

- KV may be unavailable
- the cron pipeline may not have run yet
- the last execution summary may have expired from KV

If `/cron` returns `Cron Secret 未配置`:

- the `cronSecret` field in MiSub settings has not been saved yet

If external schedulers get `401 Unauthorized`:

- verify the `secret` query parameter exactly matches the saved `cronSecret`
