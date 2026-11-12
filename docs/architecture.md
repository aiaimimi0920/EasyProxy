# 架构与配置权威

本文按主题集中维护相关合同，按目录查阅；不包含后续阶段计划。

## 目录
- [architecture](#architecture)
- [topology](#topology)
- [unified-source-architecture](#unified-source-architecture)

<a id="architecture"></a>

## Architecture

### Goal

This repository is the public monorepo for the EasyProxy stack.

### Top-Level Areas

#### `service/base`

The main EasyProxy runtime.

Responsibilities:

- management API and Web UI
- local source ownership
- manifest merge and fallback activation
- connector execution and runtime materialization
- node scoring, health checks, and export behavior

#### `upstreams/misub`

The upstream-tracked shared source registry and manifest center.

Responsibilities:

- canonical source storage with explicit `kind`
- machine manifest endpoint for `service/base`
- Cloudflare-first production deployment path
- compatibility runtime for Docker / VPS

`upstreams/misub` is a public submodule pinned from the maintained EasyProxy
fork because it defines a first-class contract for `service/base`.

#### `upstreams/aggregator`

The upstream-tracked fallback artifact producer.

Responsibilities:

- crawler and batch aggregation inputs
- published fallback subscription artifacts
- upstream sync boundary kept separate from `service/base`

#### `upstreams/ech-workers`

The upstream-tracked local ECH helper runtime.

Responsibilities:

- local helper used by connector execution in `service/base`
- upstream sync boundary for helper-side runtime logic

#### `workers/ech-workers-cloudflare`

The self-owned Cloudflare-side ECH Worker.

Responsibilities:

- public ECH Worker endpoint for managed connector profiles
- custom-domain and `workers.dev` deployment target
- Worker-side deployment code owned directly in this repository

### Fork And Submodule Boundary

`upstreams/misub`, `upstreams/aggregator`, and `upstreams/ech-workers` are
public submodules pinned to commits in forks controlled by the EasyProxy
maintainer. `workers/ech-workers-cloudflare` and `service/base` remain
first-party root code.

The root repository owns cross-module contracts, deployment automation, and
release validation. Upstream code changes land in the corresponding fork
first, then a root pull request updates only the verified submodule pointer.
Aggregator has a nested public `manager` submodule, so every checkout must use
recursive submodules.

### Deployment Assets

- `deploy/service/base`
  - deployment assets for the EasyProxy runtime
- `deploy/upstreams/misub`
  - deployment notes for the MiSub service
- `deploy/upstreams/aggregator`
  - GitHub Actions + R2 publication assets
- `deploy/upstreams/ech-workers`
  - deployment notes for the local ECH helper
- `workers/ech-workers-cloudflare`
  - deployment notes for the Cloudflare Worker

### Private Operator Boundary

Private secrets, private deployment notes, and local runtime state do not live
in this repository.

Store private operator material outside the repository in protected storage.
Tracked automation consumes non-secret topology, persistent service runtime
config, environment/platform secrets, import codes, or explicit non-secret
parameters. It must not discover credentials by scanning private archives.

<a id="topology"></a>

## Topology Contract

`topology.yaml` is the tracked, non-secret description of one EasyProxy
deployment. Copy `topology.example.yaml` and validate it with:

```powershell
go -C tools/easyproxyctl run ./cmd/easyproxyctl topology validate --file ../../topology.yaml
```

### Authority Boundaries

- `topology.yaml` owns component enablement, deterministic cloud resource
  names, public endpoints, schedules, profiles, local access mode, and release
  channel.
- Environment/platform secret stores own every credential. Topology contains
  only environment variable names.
- Cloudflare APIs own live resource identity. A deployment manifest records the
  observed IDs and versions but does not replace API discovery.
- `deploy/service/base/config.yaml` owns EasyProxy runtime settings and WebUI
  persistence. Topology does not overwrite that file during ordinary updates.

The former root `config.yaml` and derived-config renderer were removed. Local
runtime initialization copies `deploy/service/base/config.template.yaml` once;
ordinary deploys and topology updates never overwrite the resulting file.

### Deterministic Names

Empty resource-name fields are derived from `deployment_name`:

- `<deployment>-misub-pages`
- `<deployment>-misub-d1`
- `<deployment>-ech-worker`
- `<deployment>-artifacts`

Names are normalized and hash-suffixed when truncation is required. Explicit
names must still satisfy the public resource-name contract.

### Update Safety

- Bootstrap reuses an exact-name resource and creates only a missing resource.
- Update requires every enabled resource to exist and never creates a
  replacement database or bucket.
- A topology/API identity mismatch stops the update.
- Manifests contain no resolved secret values and carry a checksum.

### Lifecycle Ownership

Ordinary deployment must not introduce a second runtime-config writer.
An explicit runtime snapshot is a distribution tool, not an update authority.
`easyproxyctl` owns topology validation, resource naming, reconciliation rules,
and deployment-manifest integrity. Provider scripts and workflows call that
contract; they must not implement a second naming or create-on-update policy.

### Deployment Manifest

Build a secret-free manifest after resources and immutable images have been
resolved. Source commits are collected from Git automatically; explicit
overrides support source-less release jobs:

```powershell
go -C tools/easyproxyctl run ./cmd/easyproxyctl manifest build `
  --topology ../../topology.yaml `
  --repo-root ../.. `
  --resource-id d1=<database-id> `
  --image easyproxy=ghcr.io/<owner>/<image>@sha256:<digest> `
  --output ../../deployment-manifest.json
```

Only enabled components/resources are emitted. The manifest records component
selection, root and recursive submodule commits, supplied provider IDs, supplied
immutable image references, workflow run, topology hash, and its own checksum.

<a id="unified-source-architecture"></a>

## Unified Source Architecture

### Purpose

This document defines the unified-source model for the EasyProxy monorepo. The
goal is to let `upstreams/misub`, `service/base`, and `upstreams/aggregator`
cooperate without collapsing all inputs into a single legacy URL heuristic.

### Source Taxonomy

The canonical source model uses the following fields:

- `id`
- `kind`
- `name`
- `enabled`
- `group`
- `notes`
- `input`
- `options`

The manifest contract also allows auxiliary probe metadata for operator feedback:

- `probe_status`
- `detected_kind`
- `last_probe_at`
- `probe_message`
- `probe_input`

The manifest contract defines three kinds:

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

### Layer Responsibilities

#### `upstreams/aggregator`

- accepts subscription-oriented upstream sources
- produces standard subscription artifacts
- acts as the fallback artifact producer
- is not a generic source registry

#### `upstreams/misub`

- is the global source registry
- is deployed with Cloudflare Pages + D1 as the primary production path
- stores `subscription`, `proxy_uri`, and `connector` entries with explicit
  `kind`
- can auto-sync `upstreams/aggregator` exported subscriptions from
  `<aggregator-public-base>/internal/crawledsubs.json` as internal discovery
  sources
- re-probes those internal discovery sources inside MiSub during aggregator
  sync as a redundant operator-safety check
- maintains a dedicated stable source bound to the Aggregator stable manifest
- maintains `aggregator-global` as the managed public profile seeded by the
  stable source and able to retain deployment-managed runtime proxy URIs plus
  configured connector references
- keeps ordinary `/sub` output behavior for normal clients
- exposes a machine-facing manifest endpoint for `service/base`

#### `service/base`

- is the local execution plane and final proxy exit
- keeps local sources under local operator control
- merges local sources with `upstreams/misub` manifest sources at runtime
- activates aggregator fallback subscriptions only when the manifest fetch
  fails
- executes supported manifest connectors locally and materializes them into
  local proxy URIs before reload
- supports source-sync-only bootstrap so a pure manifest/connector deployment
  can start even before any local seed nodes exist

### Allowed Inputs By Layer

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

### Runtime Merge Contract

Runtime precedence in `service/base` is:

1. local
2. `upstreams/misub` manifest
3. `upstreams/aggregator` fallback

Dedupe is based on normalized `(kind, input)`.

Remote manifest sources and fallback sources are runtime-only sources:

- they do not overwrite the local persistent node store
- they are rebuilt on each sync cycle
- fallback sources deactivate automatically after manifest recovery

### MiSub Manifest v1

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

When a provider is reachable only through the existing healthy pool, operators
may add its exact runtime source reference to `pool.detour_source_refs`, for
example `manifest:conn_zenproxy_primary`. EasyProxy then:

- leaves matching HTTP proxy nodes on direct dialing
- dials matching non-HTTP nodes through an internal `proxy-bootstrap` pool
- excludes every node from the matching source, including its HTTP nodes, from
  that bootstrap pool so the chain cannot recurse into the same provider
- rejects the candidate configuration if a detoured node exists but there is no
  independent bootstrap member

An absent or empty list preserves direct dialing for every source. This is an
explicit NAS/egress compatibility control, not an automatic rewrite based on a
provider display name or a transient health result.

Profile lookup accepts either:

- the stored profile `id`
- the profile `customId`

`/sub` remains the ordinary client-facing subscription endpoint.

### Normalization Rules

- `http://user:pass@host:port` is a `proxy_uri`, not a `subscription`
- bare `user:pass@host:port` is normalized to `http://user:pass@host:port`
- `socks5://user:pass@host:port` remains `proxy_uri`
- local `subscriptions[]` in `service/base` remain local `subscription` sources
- local `nodes[]`, `nodes_file`, and manual nodes in `service/base` remain
  local `proxy_uri` sources

### Ambiguous HTTP(S) Sources

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

### Fallback Contract

`upstreams/aggregator` produces one canonical R2-backed subscription artifact
per fallback profile.

Canonical public fallback shape:

- `<aggregator-public-base>/subs/effective.txt`

The operator-specific base URL comes from topology. Publication writes
`manifests/stable.json` last and retains the previous version under
`last-known-good/`; see [`cloud-operations.md#aggregator-publication`](cloud-operations.md#aggregator-publication).

Ownership rule:

- the fallback URL is configured locally in
  `service/base.source_sync.fallback_subscriptions`
- `upstreams/misub` does not own that fallback URL because fallback must still
  work when `upstreams/misub` is unavailable

### Current Boundary

Current implemented state:

- `subscription` + `proxy_uri` end-to-end support
- `upstreams/misub` registry-level support for `connector` metadata, including
  `ECH Worker` and `ZenProxy client providers`
- explicit `upstreams/misub` manifest endpoint
- `service/base` runtime merge and fallback activation
- `service/base` local lifecycle management for `connector_type = ech_worker`
- `service/base` remote fetch + materialization for `connector_type = zenproxy_client`
