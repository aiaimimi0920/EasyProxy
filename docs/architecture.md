# Architecture

## Goal

This repository is the public monorepo for the EasyProxy stack. It replaces the
older multi-repository source workspace with a single contributor-facing
repository.

## Top-Level Areas

### `service/base`

The main EasyProxy runtime.

Responsibilities:

- management API and Web UI
- local source ownership
- manifest merge and fallback activation
- connector execution and runtime materialization
- node scoring, health checks, and export behavior

### `upstreams/misub`

The upstream-tracked shared source registry and manifest center.

Responsibilities:

- canonical source storage with explicit `kind`
- machine manifest endpoint for `service/base`
- Cloudflare-first production deployment path
- compatibility runtime for Docker / VPS

`upstreams/misub` remains fork-derived code kept inside the monorepo because it
defines a first-class contract for `service/base`.

### `upstreams/aggregator`

The upstream-tracked fallback artifact producer.

Responsibilities:

- crawler and batch aggregation inputs
- published fallback subscription artifacts
- upstream sync boundary kept separate from `service/base`

### `upstreams/ech-workers`

The upstream-tracked local ECH helper runtime.

Responsibilities:

- local helper used by connector execution in `service/base`
- upstream sync boundary for helper-side runtime logic

### `workers/ech-workers-cloudflare`

The self-owned Cloudflare-side ECH Worker.

Responsibilities:

- public ECH Worker endpoint for managed connector profiles
- custom-domain and `workers.dev` deployment target
- Worker-side deployment code owned directly in this repository

## Why There Are No Root Submodules

Submodules make external contribution flow harder:

- contributors must discover multiple repositories
- pull request destination becomes ambiguous
- cross-module changes become difficult to review

This monorepo keeps all public contribution in one place while preserving clear
internal boundaries between modules.

## Deployment Assets

- `deploy/service/base`
  - deployment assets for the EasyProxy runtime
- `deploy/upstreams/misub`
  - deployment notes for the MiSub service
- `deploy/upstreams/aggregator`
  - GitHub Actions + R2 publication assets
- `deploy/upstreams/ech-workers`
  - deployment notes for the local ECH helper
- `deploy/workers/ech-workers-cloudflare`
  - deployment notes for the Cloudflare Worker

## Private Operator Boundary

Private secrets, private deployment notes, and local runtime state do not live
in this repository.

Those materials stay under the shared `AIRead` knowledge base and may be linked
locally into the repo root for operator convenience, but they must remain
ignored by Git.
