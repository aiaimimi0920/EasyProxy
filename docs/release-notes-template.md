# Release Notes Template

Use this template when preparing a public GitHub release for `EasyProxy`.

## Summary

Short paragraph describing what changed in this release and who should care.

Example:

`EasyProxy` now ships the full stack from a single public monorepo: native
aggregator publishing, MiSub Pages deploy, Cloudflare ECH worker deploy, GHCR
images, and private `service/base` config distribution.

## Included In This Release

- `service/base`
  - runtime / management API changes
- `upstreams/misub`
  - manifest center / source registry changes
- `upstreams/aggregator`
  - crawler / published artifact changes
- `workers/ech-workers-cloudflare`
  - worker protocol or deployment changes
- `deployment / CI`
  - workflow, GHCR, or config distribution changes

## Deployment Notes

- GHCR images:
  - `ghcr.io/<owner>/easy-proxy-monorepo-service:<tag>`
  - `ghcr.io/<owner>/ech-workers-monorepo:<tag>`
- private config distribution:
  - manifest key: `service-base/manifest.json`
  - config key: `service-base/config.yaml`
- Cloudflare:
  - MiSub Pages project: `misub-git`
  - ECH worker: `proxyservice-ech-workers`

## Validation

Reference the successful workflow runs used to validate the release:

- `Validate`
- `Deploy Aggregator`
- `Deploy Cloudflare Apps`
- `Publish GHCR Images`
- `Publish Service Base Config`

## Operator Upgrade Notes

- If `service/base` config distribution changed:
  - regenerate bootstrap JSON or import code
- If Cloudflare secrets rotated:
  - rerun `Deploy Cloudflare Apps`
- If aggregator crawler inputs changed:
  - rerun `Deploy Aggregator`

## Breaking Changes

List only if applicable.

- Removed deprecated workflow or secret names
- Changed public artifact URL
- Changed runtime config schema
- Changed connector defaults

## Known Limitations

List only active operator-facing caveats that still matter after the release.
