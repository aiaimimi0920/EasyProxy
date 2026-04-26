# Phase 04: Documentation Cleanup

## Goal

Normalize imported repository-level documentation away from legacy workspace
paths and old multi-repo wording while preserving useful historical import
context where needed.

## Completed

- rewrote repository-level docs to describe the project as an `EasyProxy`
  monorepo rather than as a `ProxyService` workspace
- updated deploy notes to reference:
  - `service/base`
  - `upstreams/misub`
  - `upstreams/aggregator`
  - `upstreams/ech-workers`
  - `workers/ech-workers-cloudflare`
- updated deploy examples and API examples to use the monorepo runtime
  contract:
  - management port `29888`
  - pool listener `22323`
  - multi-port examples in the `25000+` range
  - monorepo service/image names
- replaced stale `deploy/EasyProxy/...` script references with
  `deploy/service/base/...`
- replaced stale `repos/...` references in deploy notes with monorepo-native
  module paths where those docs are meant to guide current operation

## Intentionally Retained Historical References

- migration mapping docs still mention legacy source paths such as
  `repos/EasyProxy` where that history is the point of the document
- active production endpoints that still include `proxyservice` in their domain
  names remain unchanged because those are real deployed addresses, not stale
  repo paths
- some operator scripts still carry compatibility lookups against legacy
  private archive locations; that is follow-up work, not repository prose

## Verification

- repository-level doc search now only returns legacy path names in intentional
  migration mapping sections, active endpoint names, or compatibility-oriented
  scripts
- the legacy source workspace remains untouched
