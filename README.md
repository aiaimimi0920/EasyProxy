# EasyProxy

EasyProxy is the public monorepo entrypoint for the EasyProxy stack.

It replaces the older multi-repository source workspace with a single
contributor-facing repository while preserving explicit boundaries between the
main runtime, the shared manifest registry, upstream-tracked modules, and
deployment assets.

This repository intentionally avoids root-level submodules. External
contributors only need one repository and one pull request target.

## Repository Layout

```text
service/
  base/
upstreams/
  misub/
  aggregator/
  ech-workers/
workers/
  ech-workers-cloudflare/
deploy/
  service/
    base/
  upstreams/
    misub/
    aggregator/
    ech-workers/
  workers/
    ech-workers-cloudflare/
docs/
scripts/
api/
```

## Module Roles

### `service/base`

The main EasyProxy runtime.

Responsibilities:

- local proxy runtime and management API
- source merge logic for local, manifest, and fallback inputs
- local connector execution for supported connector sources
- node scoring, health checks, and export surfaces

### `upstreams/misub`

The upstream-tracked shared source registry and manifest center that powers
`service/base`.

Responsibilities:

- source registry for `subscription`, `proxy_uri`, and `connector`
- machine manifest endpoint for `service/base`
- Cloudflare Pages + D1 primary deployment path
- Docker / VPS compatibility path

### `upstreams/aggregator`

The upstream-tracked fallback artifact producer.

Responsibilities:

- crawler and batch aggregation inputs
- published fallback subscription artifacts
- upstream sync boundary kept narrow and reviewable

### `upstreams/ech-workers`

The upstream-tracked local ECH connector helper.

Responsibilities:

- local helper runtime used by `service/base` connector execution
- upstream sync boundary for local ECH helper logic

### `workers/ech-workers-cloudflare`

The self-owned Cloudflare-side ECH entrypoint worker.

Responsibilities:

- public Worker endpoint for managed ECH connector profiles
- Cloudflare deployment code owned directly in this monorepo

## Quick Start

### EasyProxy runtime

```powershell
Set-Location service/base/frontend
npm ci
npm run build

Set-Location ..
go mod download
go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" -o easy-proxy ./cmd/easy_proxies
```

### MiSub runtime

```powershell
Set-Location upstreams/misub
npm install
npm run build
```

### Deployment Assets

Read the module-specific deployment notes:

- `deploy/service/base`
- `deploy/upstreams/misub`
- `deploy/upstreams/aggregator`
- `deploy/workers/ech-workers-cloudflare`

## Documentation

- `docs/architecture.md`
- `docs/quickstart.md`
- `docs/unified-source-architecture.md`
- `docs/upstream-sync.md`
- `docs/migration-plan.md`
- `CONTRIBUTING.md`

## Private Operator Material

Private deployment notes, secrets, and runtime state do not belong in this
repository.

Local operator material should continue to live under the shared `AIRead`
knowledge base and remain ignored by Git when linked into this repository.
