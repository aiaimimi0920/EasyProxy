# Upstream Sync

## Purpose

This repository keeps `upstreams/aggregator` and `upstreams/ech-workers`
inside the monorepo so public contributors only need one repository. They are
still maintained as distinct upstream sync boundaries.

`upstreams/misub` is also an upstream-tracked boundary. Even though it acts as
the shared manifest center for `service/base`, its maintenance model is still
fork + upstream sync, so it stays under `upstreams/*`.

## Rule

External contributors open pull requests here. Maintainers decide whether a
change should:

- stay as a monorepo-local patch
- be carried on top of upstream
- be proposed back to the upstream project separately

## Maintainer Workflow

1. Sync upstream-derived source in a dedicated maintainer workspace.
2. Resolve conflicts there first.
3. Copy or import the reviewed result into the corresponding monorepo boundary.
4. Keep local patches narrow and easy to identify.
5. Document intentional divergence in the pull request summary.

## Guardrails

- Do not scatter `aggregator` code into `service/base`.
- Do not scatter `ech-workers` code into `service/base`.
- Keep deployment config sanitized and public-safe inside `deploy/`.
- Keep private credentials and environment values out of imported source trees.
- If `upstreams/misub` is resynced from its maintained fork, explain which
  local monorepo patches were retained on top.

## Self-Owned Areas

`workers/ech-workers-cloudflare` is self-owned in this repository. It does not
have to follow the upstream-import workflow above, but deployment notes and
private credentials must still stay separated.
