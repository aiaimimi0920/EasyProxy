# Unified Source Architecture

## Purpose

This document defines the unified-source model for the EasyProxy monorepo. The
goal is to let `upstreams/misub`, `service/base`, and `upstreams/aggregator`
cooperate without collapsing all inputs into a single legacy URL heuristic.

## Source Taxonomy

The canonical source model uses the following fields:

- `id`
- `kind`
- `name`
- `enabled`
- `group`
- `notes`
- `input`
- `options`

Phase 1 also allows auxiliary probe metadata for operator feedback:

- `probe_status`
- `detected_kind`
- `last_probe_at`
- `probe_message`
- `probe_input`

Phase 1 defines three kinds:

- `subscription`
  - `input` is an `http://` or `https://` subscription URL
- `proxy_uri`
  - `input` is a normalized direct proxy URI such as `vmess://`,
    `vless://`, `trojan://`, `ss://`, `socks5://`, or `http://`
  - bare residential inputs such as `user:pass@host:port` normalize to
    `http://user:pass@host:port`
- `connector`
  - used by `upstreams/misub` to carry connector metadata such as `ECH Worker`
    or provider-backed runtime sources like `ZenProxy`
  - currently executed locally by `service/base` when
    `connector_type = ech_worker` or `connector_type = zenproxy_client`

## Layer Responsibilities

### `upstreams/aggregator`

- accepts subscription-oriented upstream sources
- produces standard subscription artifacts
- acts as the fallback artifact producer
- does not become a generic source registry in Phase 1

### `upstreams/misub`

- is the global source registry
- is deployed with Cloudflare Pages + D1 as the primary production path
- stores `subscription`, `proxy_uri`, and `connector` entries with explicit
  `kind`
- can auto-sync `upstreams/aggregator` exported subscriptions from
  `https://sub.aiaimimi.com/internal/crawledsubs.json` as internal discovery
  sources
- re-probes those internal discovery sources inside MiSub during aggregator
  sync as a redundant operator-safety check
- maintains a dedicated stable source for
  `https://sub.aiaimimi.com/subs/clash.yaml`
- maintains `aggregator-global` as the managed public profile seeded by the
  stable source and able to retain deployment-managed runtime proxy URIs plus
  configured connector references
- keeps ordinary `/sub` output behavior for normal clients
- exposes a machine-facing manifest endpoint for `service/base`

### `service/base`

- is the local execution plane and final proxy exit
- keeps local sources under local operator control
- merges local sources with `upstreams/misub` manifest sources at runtime
- activates aggregator fallback subscriptions only when the manifest fetch
  fails
- executes supported manifest connectors locally and materializes them into
  local proxy URIs before reload
- supports source-sync-only bootstrap so a pure manifest/connector deployment
  can start even before any local seed nodes exist

## Allowed Inputs By Layer

- `upstreams/aggregator`
  - `subscription`
- `upstreams/misub`
  - `subscription`
  - `proxy_uri`
  - `connector` metadata such as `ECH Worker`
- `service/base`
  - local `subscription`
  - local `proxy_uri`
  - remote `subscription`, `proxy_uri`, and supported `connector` sources from
    `upstreams/misub` manifest
  - fallback `subscription` from `upstreams/aggregator`

This intentional overlap is part of the design:

- adding a source in `upstreams/misub` affects all `service/base` consumers
- adding a source in `service/base` affects only the local runtime
- adding a source in `upstreams/aggregator` keeps a fallback subscription
  artifact available even when `upstreams/misub` is down

## Runtime Merge Contract

Runtime precedence in `service/base` is:

1. local
2. `upstreams/misub` manifest
3. `upstreams/aggregator` fallback

Dedupe is based on normalized `(kind, input)`.

Remote manifest sources and fallback sources are runtime-only sources:

- they do not overwrite the local persistent node store
- they are rebuilt on each sync cycle
- fallback sources deactivate automatically after manifest recovery

## MiSub Manifest v1

`upstreams/misub` exposes:

- `GET /api/manifest/:profileId`

Auth contract:

- `Authorization: Bearer <MANIFEST_TOKEN>`

Storage stance:

- Cloudflare production defaults to D1
- KV remains compatibility / migration only
- Docker / VPS compatibility uses SQLite with the same serialized source model

Response shape:

```json
{
  "success": true,
  "version": "v1",
  "generated_at": "2026-03-24T00:00:00.000Z",
  "profile": {
    "id": "profile-id",
    "customId": "custom-profile",
    "name": "Default"
  },
  "sources": [
    {
      "id": "source-1",
      "kind": "subscription",
      "name": "shared-subscription",
      "enabled": true,
      "group": "",
      "notes": "",
      "input": "https://example.com/sub",
      "options": {}
    }
  ]
}
```

Current manifest behavior:

- selected `subscription` sources are emitted directly
- selected `proxy_uri` sources are emitted directly
- selected `connector` sources are also emitted, including
  `options.connector_type` and `options.connector_config`
- downstream runtime execution is currently implemented in `service/base` for
  `connector_type = ech_worker` and `connector_type = zenproxy_client`

`zenproxy_client` connector contract:

- `input` points at the ZenProxy server base URL or direct fetch endpoint
- `options.connector_config.api_key` stores the ZenProxy user API key
- optional filters such as `count`, `country`, `type`, `proxy_id`, `chatgpt`,
  `google`, `residential`, and `risk_max` are forwarded to
  `/api/client/fetch`
- `service/base` converts each returned sing-box outbound into a runtime proxy
  URI and treats the result as ephemeral manifest-origin nodes

Profile lookup accepts either:

- the stored profile `id`
- the profile `customId`

`/sub` remains the ordinary client-facing subscription endpoint.

## Normalization Rules

- `http://user:pass@host:port` is a `proxy_uri`, not a `subscription`
- bare `user:pass@host:port` is normalized to `http://user:pass@host:port`
- `socks5://user:pass@host:port` remains `proxy_uri`
- local `subscriptions[]` in `service/base` remain local `subscription` sources
- local `nodes[]`, `nodes_file`, and manual nodes in `service/base` remain
  local `proxy_uri` sources

## Ambiguous HTTP(S) Sources

`kind` remains the primary classification field.

For ambiguous `http(s)` sources, `upstreams/misub` uses a best-effort probe as a
helper:

- if the input shape already clearly looks like a direct HTTP(S) proxy,
  `upstreams/misub` skips the network probe and records a structural hint
- otherwise `upstreams/misub` attempts one fetch
- if the response looks like a subscription payload, it records
  `detected_kind = subscription`
- if the response behaves like a proxy entrypoint, it records
  `detected_kind = proxy_uri`
- if the request fails or the response is still ambiguous, it records an
  inconclusive status

Important boundary:

- probe metadata is advisory only
- probe metadata must not auto-rewrite the saved `kind`
- operators may keep a manually chosen `kind` even when the probe disagrees
- save flows may refresh probe metadata automatically when the source input
  changes
- edit modals expose a manual "re-probe" action so operators can refresh probe
  metadata without saving unrelated edits
- list views also expose one-click and batch re-probe actions for already-saved
  sources
- re-probe actions update local operator data and should be followed by a
  normal save when persistence is desired

## Fallback Contract

`upstreams/aggregator` produces one canonical R2-backed subscription artifact
per fallback profile.

Current canonical public fallback URL:

- `https://sub.aiaimimi.com/subs/clash.yaml`

Ownership rule:

- the fallback URL is configured locally in
  `service/base.source_sync.fallback_subscriptions`
- `upstreams/misub` does not own that fallback URL because fallback must still
  work when `upstreams/misub` is unavailable

## Current Boundary

Current implemented state:

- `subscription` + `proxy_uri` end-to-end support
- `upstreams/misub` registry-level support for `connector` metadata, including
  `ECH Worker` and `ZenProxy client providers`
- explicit `upstreams/misub` manifest endpoint
- `service/base` runtime merge and fallback activation
- `service/base` local lifecycle management for `connector_type = ech_worker`
- `service/base` remote fetch + materialization for `connector_type = zenproxy_client`
