# Module Inventory

## Source Ownership

| Role | Origin | Target | Current ownership |
| --- | --- | --- | --- |
| Main runtime | first-party | `service/base` | tracked in the root repository |
| Shared manifest center | `imzyb/MiSub` | `upstreams/misub` | public submodule pinned to `aiaimimi0920/MiSub` |
| Upstream fallback producer | `wzdnzd/aggregator` | `upstreams/aggregator` | public submodule pinned to `aiaimimi0920/aggregator` |
| Upstream local ECH helper | `hhsw2015/ech-workers` | `upstreams/ech-workers` | public submodule pinned to `aiaimimi0920/ech-workers` |
| Self-owned Worker | first-party | `workers/ech-workers-cloudflare` | tracked in the root repository |

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
- private Git remotes, credentials, and maintainer worktrees

These remain external operator assets. Public submodule metadata and pinned
commits are intentionally tracked by the root repository.
