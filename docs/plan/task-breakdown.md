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

## Task 4: Fork And Submodule Workflow

- sync official upstream changes inside each maintained fork
- validate and merge the fork pull request first
- update one root submodule pointer in a separate pull request
- require recursive checkout and cross-module validation

## Task 5: Verification

- verify target layout
- verify excluded local-only content did not get imported
- verify new docs reference the monorepo structure
- verify a credential-free recursive clone initializes every public submodule
