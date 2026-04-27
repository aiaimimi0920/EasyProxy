# Aggregator GitHub Actions + R2

`deploy/upstreams/aggregator` now only contains the assets that are still
needed after
the cutover away from Cloudflare Workers and Containers.

Current runtime split:

- GitHub Actions on `ubuntu-latest`
  - runs `subscribe/process.py --overwrite`
  - performs crawler and batch aggregation
  - writes artifacts directly into the `aggregator` R2 bucket
- R2 custom domain
  - serves published artifacts directly from `https://sub.aiaimimi.com`

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

Published artifacts are now read directly from the bucket custom domain:

- `https://sub.aiaimimi.com/subs/clash.yaml`
- `https://sub.aiaimimi.com/subs/v2ray.txt`
- `https://sub.aiaimimi.com/subs/singbox.json`
- `https://sub.aiaimimi.com/subs/mixed.txt`
- `https://sub.aiaimimi.com/internal/crawledsubs.json`

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
  the shared private deployment notes for the EasyProxy stack.
- The legacy external-repository dispatch path is retired; the active
  deployment and verification baseline is the native GitHub Actions batch flow above.
