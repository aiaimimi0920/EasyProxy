# Module Inventory

## Copy Targets

| Role | Source Snapshot | Target | Notes |
| --- | --- | --- | --- |
| Main runtime | legacy `repos/EasyProxy` | `service/base` | canonical EasyProxy runtime |
| Shared manifest center | legacy `repos/MiSub` | `upstreams/misub` | upstream-tracked source registry and manifest center |
| Upstream fallback producer | legacy `repos/aggregator` | `upstreams/aggregator` | upstream-tracked boundary |
| Upstream local ECH helper | legacy `repos/ech-workers` | `upstreams/ech-workers` | upstream-tracked boundary |
| Self-owned Worker | legacy `repos/ech-workers-cloudflare` | `workers/ech-workers-cloudflare` | Cloudflare-side runtime |

## Deployment Targets

| Role | Source Snapshot | Target | Exclusions |
| --- | --- | --- | --- |
| EasyProxy deploy assets | legacy `deploy/EasyProxy` | `deploy/service/base` | exclude `config.yaml`, `data/` |
| MiSub deploy notes | legacy `deploy/MiSub` | `deploy/upstreams/misub` | none |
| aggregator deploy assets | legacy `deploy/aggregator` | `deploy/upstreams/aggregator` | none |
| ech-workers deploy notes | legacy `deploy/ech-workers` | `deploy/upstreams/ech-workers` | none |
| Worker deploy notes | legacy `deploy/ech-workers-cloudflare` | `deploy/workers/ech-workers-cloudflare` | none |

## Private Material Boundary

The following are not part of the tracked monorepo migration:

- the shared `AIRead/密钥/*` archive outside this repository
- the shared `AIRead/部署/*` archive outside this repository
- local runtime config and state
- local `.git` metadata from the source repositories

These remain external operator assets.
