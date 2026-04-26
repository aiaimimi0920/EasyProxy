# Task Breakdown

## Task 1: Monorepo Bootstrap

- initialize the empty target repository
- create the top-level directory skeleton
- add root documentation and ignore rules

## Task 2: Structural Import

- import `EasyProxy` into `service/base`
- import `MiSub` into `upstreams/misub`
- import `aggregator` and `ech-workers` into `upstreams/*`
- import `ech-workers-cloudflare` into `workers/*`

## Task 3: Deployment Asset Import

- import deploy assets into the mirrored `deploy/` structure
- exclude live runtime state and local config

## Task 4: Repeatable Sync Workflow

- add a root script that replays the copy-based migration
- encode exclusions and mapping rules in the script

## Task 5: Verification

- verify target layout
- verify excluded local-only content did not get imported
- verify new docs reference the monorepo structure
