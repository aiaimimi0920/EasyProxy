# MiSub Deploy Notes

This directory stores deployment-facing notes for `MiSub` in the `EasyProxy`
monorepo.

Source code now lives in:

- `upstreams/misub`

## Production Path

`MiSub` is the global source registry and manifest center.

Formal production recommendation:

- Cloudflare Pages + Functions
- D1 as the primary persistent store
- aggregator discovery sync source:
  - `https://sub.aiaimimi.com/internal/crawledsubs.json`
- aggregator stable public source:
  - `https://sub.aiaimimi.com/subs/clash.yaml`

Current deployed instance:

- Pages project: `misub-git`
- public URL: `https://misub.aiaimimi.com`
- Pages fallback URL: `https://misub-git.pages.dev`
- legacy direct-upload project `misub` deleted on `2026-03-26`
- local deployment config file:
  - `upstreams/misub/wrangler.jsonc`
- reference-only manual Pages sample:
  - `upstreams/misub/wrangler-cf-pages.toml`

Local Docker/VPS coexistence default in this monorepo:

- container name:
  - `misub-monorepo`
- host port:
  - `18080`
- container port:
  - `8080`

Root operator entrypoint:

- `scripts/deploy-misub.ps1`
  - default mode:
    - Cloudflare Pages
  - optional local mode:
    - `-Mode docker`

KV is no longer the recommended primary backend. Keep it only when you need:

- legacy read compatibility
- KV -> D1 migration
- temporary rollback while D1 is unavailable

## Required Cloudflare Runtime Contract

Required bindings / variables:

- `MISUB_DB`
- `ADMIN_PASSWORD`
- `COOKIE_SECRET`
- `MANIFEST_TOKEN`

Optional but supported:

- `MISUB_KV`
- `MISUB_PUBLIC_URL`
- `MISUB_CALLBACK_URL`
- `CORS_ORIGINS`

## Machine API

Machine consumers use:

- `GET /api/manifest/:profileId`

Auth contract:

- `Authorization: Bearer <MANIFEST_TOKEN>`

Lookup rules:

- `:profileId` may be the stored profile `id`
- `:profileId` may be the stored profile `customId`

Phase 1 output rules:

- includes enabled `subscription` sources
- includes enabled `proxy_uri` sources
- includes enabled `connector` metadata sources such as `ECH Worker`
- connector emission does not imply local runtime execution

## Operator Cron API

External schedulers still hit:

- `GET /cron?secret=<cronSecret>`

Authenticated operators may also use:

- `GET /api/cron/status`
- `POST /api/cron/trigger`
- `GET /cron-dashboard`

Behavior notes:

- `/cron` remains the secret-protected execution entrypoint intended for
  external uptime / scheduler services
- `/api/cron/status` reads the latest persisted execution summary when KV is
  available
- `/api/cron/trigger` reuses the same cron execution pipeline but is protected
  by the normal admin session
- `/cron-dashboard` is a lightweight operator page that consumes the two
  authenticated `/api/cron/*` endpoints with the current browser session
- the dashboard setup guide lives at:
  - `upstreams/misub/docs/CRON_DASHBOARD_GUIDE.md`

## Docker / VPS Compatibility Path

Docker / VPS remains supported as a secondary runtime:

- Node server runtime
- SQLite persistence via `MISUB_DB_PATH`

Keep the Docker path feature-compatible with Cloudflare:

- same unified source schema
- same `/api/manifest/:profileId` contract
- same `MANIFEST_TOKEN` requirement
- same aggregator auto-sync behavior when `/cron` or manual sync is used

## EasyProxy Integration

`EasyProxy` should point to:

- `source_sync.manifest_url`
- `source_sync.manifest_token`

`MiSub` does not own the aggregator fallback URL. Keep fallback locally in:

- `EasyProxy.source_sync.fallback_subscriptions`

Managed ECH connector note:

- the target Worker entrypoint for machine-managed connector profiles is:
  - `https://proxyservice-ech-workers.aiaimimi.com:443`
- `workers.dev` remains a rollback/debug entrypoint and should not be treated
  as the preferred long-term machine URL once the custom domain is deployed
- preferred IP write-back for the managed profile is handled by:
  - `deploy/service/base/scripts/update_ech_preferred_ips.ps1`

## Aggregator Integration Model

The current `MiSub` deployment treats aggregator outputs as two different
layers:

- `crawledsubs.json`
  - synced into `MiSub` as internal discovery sources
  - re-probed by `MiSub` during each aggregator sync run
  - marked as sync-managed discovery records
  - not exposed by default through the managed public profile
- `clash.yaml`
  - maintained as a dedicated managed stable source
  - used by the managed public profile `aggregator-global`

Operationally, this means the default public profile should expose the stable
aggregated artifact, while any deployment-managed runtime proxy nodes and
configured connector references can be layered on top of that managed profile.
The raw crawler-discovered sources remain available for operator review and
selective reuse.

## Accepted Global Inputs

`MiSub` now accepts all three source families needed by the current stack:

- public hand-entered subscription URLs
- public hand-entered residential / direct proxy inputs such as
  `user:pass@host:port`
- `ECH Worker` connector metadata stored as `kind=connector`
