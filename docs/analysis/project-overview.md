# Project Overview

## Source State

The legacy source workspace is an orchestration root around five standalone
repositories:

- `repos/EasyProxy`
- `repos/MiSub`
- `repos/aggregator`
- `repos/ech-workers`
- `repos/ech-workers-cloudflare`

It also carries:

- `deploy/`
- `docs/`
- `AIRead/`

This means the legacy source tree was already a product stack, but not yet a
single contributor-facing repository.

## Migration Intent

The target `EasyProxy` repository should become the public monorepo entrypoint
for this stack.

The migration must be copy-only:

- keep the source workspace unchanged
- reconstruct the stack in `EasyProxy`
- preserve explicit boundaries between the main runtime, companion service,
  upstream-derived modules, and self-owned workers

## Reference Pattern

`EasyEmail` provides the reference shape:

- one public monorepo
- no root submodules
- clear top-level areas such as `service/`, `upstreams/`, `deploy/`, and
  `docs/`
- explicit upstream sync guidance instead of hidden workspace conventions

## Resulting Direction

For `EasyProxy`, the main runtime is `service/base`, the manifest registry is
kept as `upstreams/misub`, other upstream-derived modules stay under
`upstreams/*`, and the Cloudflare Worker sidecar becomes `workers/*`.
