# Dependency Graph

## Structural Dependencies

1. `service/base`
   - depends on `upstreams/misub` manifest contract
   - depends on `upstreams/aggregator` fallback artifact contract
   - depends on `upstreams/ech-workers` for local ECH connector execution
   - depends on `workers/ech-workers-cloudflare` as the managed remote Worker
     target for some connector profiles

2. `upstreams/misub`
   - depends on `upstreams/aggregator` artifacts for discovery sync and stable
     fallback profile sourcing

3. `deploy/service/base`
   - depends on `service/base`
   - operationally references `upstreams/misub`, `upstreams/aggregator`, and
     `upstreams/ech-workers`

4. `deploy/workers/ech-workers-cloudflare`
   - depends on `workers/ech-workers-cloudflare`

## Migration Ordering

Historical import ordering was bootstrap, source import, deployment import, and
structure verification. Current upstream update ordering is:

1. sync and validate the maintained upstream fork
2. open and merge the fork pull request
3. update one root submodule pointer
4. run recursive cross-module validation
5. merge and publish from the root repository

## Checkout Dependency

All three `upstreams/*` source paths require submodule initialization.
Aggregator also requires its nested `manager` submodule. CI and local operators
must use `git submodule update --init --recursive`; GitHub Actions checkout steps
must set `submodules: recursive`.
