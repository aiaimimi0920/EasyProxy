# Phase 02: Structural Import

## Completed

- copied the legacy `EasyProxy` runtime into `service/base`
- copied the legacy `MiSub` runtime into `upstreams/misub`
- copied the legacy `aggregator` runtime into `upstreams/aggregator`
- copied the legacy `ech-workers` runtime into `upstreams/ech-workers`
- copied the legacy `ech-workers-cloudflare` runtime into
  `workers/ech-workers-cloudflare`
- copied deploy assets into the mirrored `deploy/` structure
- excluded:
  - nested `.git` directories
  - `node_modules`
  - `dist`
  - `.wrangler`
  - `deploy/service/base/config.yaml`
  - `deploy/service/base/data`

## Verification

The first import confirmed that:

- imported modules exist in the expected monorepo paths
- no nested git metadata was copied
- no local runtime state or local deploy config was copied into the tracked tree

## Next

- clean up historical path references inside imported deploy notes when useful
- design root CI and shared operator config only after the structural move is
  accepted
