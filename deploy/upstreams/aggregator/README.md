# Aggregator GitHub Actions + R2

`deploy/upstreams/aggregator` now only contains the assets that are still
needed after
the cutover away from Cloudflare Workers and Containers.

Current runtime split:

- GitHub Actions on `ubuntu-latest`
  - runs `subscribe/process.py --overwrite`
  - performs crawler and batch aggregation
  - writes only release-scoped candidate objects into the configured R2 bucket
- R2 custom domain
  - serves candidate, immutable release, stable, and last-known-good objects

Removed from this monorepo boundary:

- Cloudflare Worker source
- Wrangler deployment config
- Container Dockerfile
- Cloudflare deployment/update/init scripts

## Files Kept

- `config/config.actions.r2.json`
  - the authoritative GitHub Actions batch config
  - crawler enabled
  - `Issue #91` shared source kept as the main fallback seed
- native materialization now happens through
  [scripts/materialize-aggregator-config.py](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/scripts/materialize-aggregator-config.py)
  and repository secrets in the current EasyProxy repo

Active root workflow:

- `.github/workflows/deploy-aggregator.yml`

Upstream reference workflow kept for comparison and sync review:

- `upstreams/aggregator/.github/workflows/process-r2.yaml`

Retired legacy workflows kept only for history/manual inspection:

- `collect.yaml`
- `refresh.yaml`
- `process.yaml`

## Current Public Read Paths

Published stable artifacts are read from the fork operator's bucket custom
domain. With a base URL of `https://sub.example.com`, the canonical paths are:

- `https://sub.example.com/subs/clash.yaml`
- `https://sub.example.com/subs/v2ray.txt`
- `https://sub.example.com/subs/singbox.json`
- `https://sub.example.com/subs/mixed.txt`
- `https://sub.example.com/subs/effective.txt`
- `https://sub.example.com/internal/crawledsubs.json`
- `https://sub.example.com/manifests/stable.json`

These paths are public now. There is no Worker-side token gate anymore.

Current note:

- `internal/crawledproxies.txt` is not currently present on the public bucket
  path, so do not depend on it as a stable artifact.

## Operational Notes

- Root operator entrypoint:
  - `scripts/deploy-aggregator.ps1`
  - behavior:
    - triggers the native `deploy-aggregator.yml` workflow in this repository
- GitHub repository secrets and variables are documented in
  [docs/github-secrets.md](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/docs/github-secrets.md).
- GitHub Actions runtime and verification notes are documented in
  [`docs/aggregator-publication.md`](../../../docs/aggregator-publication.md).
- The legacy external-repository dispatch path is retired; the active
  deployment and verification baseline is the native GitHub Actions batch flow above.
