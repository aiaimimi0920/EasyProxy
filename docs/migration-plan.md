# Migration History And Current Ownership

## Goal

Record how the legacy multi-repo workspace was imported without modifying it,
and define the current ownership model used after the upstream submodule
migration.

The original bootstrap was a copy-style migration:

- source workspace stays untouched
- target monorepo is reconstructed in place
- source Git metadata and runtime state were excluded

That bootstrap is historical. Current ownership is:

- first-party runtime, Worker, integration, deploy, and docs stay in the root
  repository
- `upstreams/misub`, `upstreams/aggregator`, and `upstreams/ech-workers` are
  public submodules pinned to maintained forks
- upstream changes land in a fork pull request before the root pointer changes

## Design Rules

1. `service/base` becomes the canonical EasyProxy runtime.
2. `MiSub` stays as an explicit fork/submodule boundary under
   `upstreams/misub`.
3. upstream-derived code lives in maintained public forks and is pinned under
   `upstreams/*`.
4. self-owned Cloudflare-side runtime code lives under `workers/*`.
5. deployment assets move under `deploy/` with the same module split.
6. private deployment knowledge and secrets stay in `AIRead`, not in Git.
7. initial migration is structural, not a functional rewrite.

## Source To Target Mapping

The source-path column below is historical import metadata retained only to
describe the first copy migration.

| Source path | Target path | Historical import rule |
| --- | --- | --- |
| `repos/EasyProxy` | `service/base` | copy source tree, exclude `.git` |
| `repos/MiSub` | `upstreams/misub` | copy source tree, exclude `.git`, `node_modules`, `dist`, `.wrangler` |
| `repos/aggregator` | `upstreams/aggregator` | copy source tree, exclude `.git` |
| `repos/ech-workers` | `upstreams/ech-workers` | copy source tree, exclude `.git` |
| `repos/ech-workers-cloudflare` | `workers/ech-workers-cloudflare` | copy source tree, exclude `.git`, `.wrangler` |
| `deploy/EasyProxy` | `deploy/service/base` | copy deploy assets, exclude `config.yaml` and `data/` |
| `deploy/MiSub` | `deploy/upstreams/misub` | copy deploy notes as-is |
| `deploy/aggregator` | `deploy/upstreams/aggregator` | copy deploy assets as-is |
| `deploy/ech-workers` | `deploy/upstreams/ech-workers` | copy deploy notes as-is |
| `deploy/ech-workers-cloudflare` | `deploy/workers/ech-workers-cloudflare` | copy deploy notes as-is |

## Current Upstream Pins

| Target path | Maintained fork | Official upstream |
| --- | --- | --- |
| `upstreams/misub` | `aiaimimi0920/MiSub` | `imzyb/MiSub` |
| `upstreams/aggregator` | `aiaimimi0920/aggregator` | `wzdnzd/aggregator` |
| `upstreams/ech-workers` | `aiaimimi0920/ech-workers` | `hhsw2015/ech-workers` |

Clone with `--recurse-submodules`. Aggregator contains the nested public
`wzdnzd/proxy-manager` submodule.

## Non-Goals For Phase 1

- no codebase-wide build-system unification
- no secret migration into tracked files
- no runtime contract rewrite
- no attempt to collapse all modules into one language or one package manager
- no mutation of the original source workspace

## Execution Phases

### Phase 1: Bootstrap

- initialize empty `EasyProxy` repo
- write monorepo docs, ignore rules, and migration notes
- create the target directory skeleton

### Phase 2: Structural Copy

- copy runtime modules into the monorepo layout
- copy deployment assets into the matching `deploy/` layout
- preserve module-local files unless they are local runtime state or private
  config

### Phase 3: Repository Normalization

- add monorepo root README, contributing guide, and architecture docs
- establish fork and submodule governance for future upstream refreshes
- keep imported boundaries explicit so later sync is predictable

### Phase 4: Verification

- verify copied module locations exist
- verify excluded local-only files were not imported
- verify root docs point to the new monorepo paths

### Phase 5: Follow-Up Work (completed)

- root-level CI validates root scripts, upstream regressions, MiSub, Go,
  frontend source/generated assets, and the release contract
- deployment topology and persistent service runtime config have separate
  authorities; no shared root renderer rewrites runtime state
- active deployment notes live with their owning modules under `deploy/*`
- tracked operator scripts no longer auto-discover secrets from legacy private
  archive paths

### Phase 6: Fork And Submodule Ownership (completed)

- imported the EasyProxy integration delta into all three maintained forks
- added `UPSTREAM.md` and regression validation in each fork
- converted all three stable paths to public submodules
- enabled recursive checkout in every GitHub Actions checkout step
- removed the obsolete `scripts/sync-from-proxyservice.ps1` copy-sync path
- validated a fresh public recursive clone, including Aggregator `manager`

## Guardrails

- never write back into the legacy source workspace
- keep private config ignored
- keep upstream patches reviewable
- update upstream code in the maintained fork, not inside a detached root
  submodule checkout
- do not make functional behavior changes unless needed to complete the
  structural migration
