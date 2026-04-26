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

1. bootstrap target repo
2. import source modules
3. import deploy assets
4. add repeatable sync tooling
5. verify structure and exclusions
