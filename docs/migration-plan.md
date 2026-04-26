# Migration Plan

## Goal

Migrate the legacy multi-repo source workspace into this new `EasyProxy`
monorepo without modifying the source workspace.

This is a copy-style migration:

- source workspace stays untouched
- target monorepo is reconstructed in place
- root-level submodules are eliminated
- upstream-derived code keeps explicit sync boundaries

## Design Rules

1. `service/base` becomes the canonical EasyProxy runtime.
2. `MiSub` stays as an explicit upstream-tracked boundary under
   `upstreams/misub`.
3. upstream-derived code is copied into explicit boundaries under
   `upstreams/*`.
4. self-owned Cloudflare-side runtime code lives under `workers/*`.
5. deployment assets move under `deploy/` with the same module split.
6. private deployment knowledge and secrets stay in `AIRead`, not in Git.
7. initial migration is structural, not a functional rewrite.

## Source To Target Mapping

The source-path column below is historical import metadata retained only to
describe the first copy migration.

| Source path | Target path | Copy rule |
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
- add a repeatable sync script for future copy-based refreshes from the legacy
  source workspace
- keep imported boundaries explicit so later re-sync is predictable

### Phase 4: Verification

- verify copied module locations exist
- verify excluded local-only files were not imported
- verify root docs point to the new monorepo paths

### Phase 5: Follow-Up Work

- add root-level CI once the new repository is stable
- decide whether to introduce a shared root config model
- decide whether some deploy notes should be promoted into module-local docs

## Guardrails

- never write back into the legacy source workspace
- keep private config ignored
- keep upstream patches reviewable
- do not make functional behavior changes unless needed to complete the
  structural migration
