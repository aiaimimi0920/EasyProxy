# EasyProxy Deploy Assets

This directory contains deployment-side assets for the `EasyProxy` monorepo.

Use this directory for:

- local compose deployment templates
- deployment-specific config templates
- scripts and notes for running the service from this monorepo

Do not put real secrets here. Use:

- the shared `AIRead` archive linked locally outside Git

The source code now lives in:

- `service/base`

## Monorepo Isolation Defaults

To avoid interrupting the legacy source deployment during migration,
the default local monorepo runtime contract uses a separate identity:

- container name:
  - `easy-proxy-monorepo-service`
- local image name:
  - `easyproxy/easy-proxy-monorepo-service:local`
- pool listener:
  - `22323`
- management API / WebUI:
  - `29888`
- multi-port range:
  - `25000-25500`

These defaults are encoded in:

- `docker-compose.yaml`
- `config.template.yaml`
- `scripts/smoke-easy-proxy-docker-api.ps1`
- `scripts/validate-easy-proxy-runtime.ps1`

## Files

- `config.template.yaml`
  - sanitized baseline config using the current `EasyProxy` schema
- `config.yaml`
  - ignored local runtime config migrated from the legacy standard profile
- `Dockerfile`
  - monorepo-level container build that packages `EasyProxy`, `ech-workers`,
    `CloudflareSpeedTest`, and `cloudflared`
- `docker-entrypoint.sh`
  - runtime entrypoint that prepares `/var/lib/easy-proxy`, reads
    `EASY_PROXY_CONFIG_PATH`, can bootstrap the runtime config from private R2,
    and can optionally start local `cloudflared proxy-dns`
- `bootstrap-service-config.py`
  - runtime bootstrap helper that downloads the service config from private R2
    storage when the container starts without a mounted local config
- `docker-compose.yaml`
  - local container deployment definition
- `scripts/publish-ghcr-easy-proxy-service.ps1`
  - local publish helper for GHCR `easy-proxy-monorepo-service`
- `scripts/validate-easy-proxy-runtime.ps1`
  - full live validation script for local subscription, direct proxy, manifest,
    fallback, local connector, and manifest connector paths

## Unified Source Notes

`EasyProxy` now supports two runtime source layers:

- local sources
  - `subscriptions`
  - inline `nodes`
  - `nodes_file`
  - WebUI manual nodes
- remote source sync
  - `source_sync.manifest_url`
  - `source_sync.fallback_subscriptions`

Runtime precedence is:

1. local
2. MiSub manifest
3. aggregator fallback

Current scheduler baseline:

- `pool.mode: auto`
  - health-first automatic strategy
  - prefers higher availability-score nodes
  - uses active connections and latency as tie-breakers
- `POST /proxy/leases/report`
  - feeds task success/failure back into the node score
  - repeated failures lower the route score before a hard runtime blacklist is needed

Current shared-default aggregator fallback URL:

- `https://sub.aiaimimi.com/subs/clash.yaml`

Recommended management probe target for ECH-heavy profiles:

- `https://www.google.com/generate_204`

Remote manifest sources and fallback sources are runtime-only inputs. They are
not the same thing as the persistent local node store.

`connector` sources are now supported at runtime as well:

- `MiSub` may emit `kind=connector` + `connector_type=ech_worker`
- `EasyProxy` starts a local `ech-workers` process for each active connector
- each connector is materialized into a local `socks5://127.0.0.1:<port>` or
  `http://127.0.0.1:<port>` upstream before the proxy pool reloads
- these connector-derived upstreams remain ephemeral and are rebuilt from
  manifest state on each refresh
- source-sync-only startup is supported, so `EasyProxy` can now boot from a
  pure `MiSub` manifest profile even when there are no local seed nodes yet

## Live ECH Validation

Historical note on `2026-03-26`:

- an earlier private `MiSub` profile for `easyproxies-ech-runtime` used two
  community snippet connector sources
- `EasyProxy` bootstrapped and launched local `ech-workers` helpers correctly,
  but both snippet endpoints failed real traffic with `websocket: bad handshake`
- those snippet-style sources remain historical/private-only evidence and are
  not the current healthy operator baseline

Current canonical ECH runtime baseline:

- active manifest profile:
  - `easyproxies-ech-runtime`
- active connector count:
  - `1`
- health-check target:
  - `https://www.google.com/generate_204`
- managed Worker URL target:
  - `https://proxyservice-ech-workers.aiaimimi.com:443`
- rollback/debug Worker URL:
  - `https://proxyservice-ech-workers.vmjcv666.workers.dev:443`
- steady-state result:
  - pool listener returned `204`
  - dedicated node port returned `204`
  - the managed connector reported `available = true`
- historical transient evidence for this 2026-03-26 run was removed during
  workspace cleanup; the retained operator baseline is the validated runtime
  behavior summarized below and in `docs/progress/`

## Full Runtime Validation

Full live validation was rerun from the packaged Docker image on `2026-04-26`
after fixing three runtime drifts:

- source-sync bootstrap now assigns hybrid multi-port listeners before initial
  startup
- local `connectors` now participate in runtime reconciliation again
- fallback bootstrap now preserves unhealthy-manifest status instead of
  incorrectly reporting `manifest_healthy=true`

Verified scenarios:

- local subscription:
  - passed
  - pool listener returned `204`
  - dedicated node ports returned `204`
- local direct proxy URI:
  - passed
  - pool listener returned `204`
  - dedicated node port returned `204`
- MiSub manifest subscription profile:
  - passed
  - manifest URL:
    - `https://misub.aiaimimi.com/api/manifest/aggregator-global`
  - manifest source count:
    - `1`
- aggregator fallback subscription:
  - passed
  - fallback source count:
    - `1`
  - fallback activated after forced manifest failure
- local ECH connector:
  - passed
  - connector instance count:
    - `1`
  - pool listener returned `204`
- manifest ECH connector profile:
  - passed
  - manifest URL:
    - `https://misub.aiaimimi.com/api/manifest/easyproxies-ech-runtime`
  - connector source count:
    - `1`
  - connector instance count:
    - `1`
  - pool listener returned `204`

Detailed local runtime evidence was intentionally pruned during workspace
cleanup after validation completed. The durable operator baseline is the
validated behavior described in this README, the progress records under
`docs/progress/`, and the published GHCR image below.

Historical published GHCR release from the source workspace:

- `ghcr.io/aiaimimi0920/easy-proxy-service:release-20260404-001`

## Recommended Flow

1. Use `config.template.yaml` as the sanitized reference shape.
2. Keep the real runtime values in `config.yaml` and the secret archive in
   `AIRead`.
3. Do not duplicate the aggregator fallback URL inside local `subscriptions`;
   keep it only under `source_sync.fallback_subscriptions`.
4. Run `docker compose up -d` from this directory.
5. Validate the management API on `29888` and the proxy listener on `22323`.

The current container path contract is:

- config mount: `/etc/easy-proxy/config.yaml`
- writable runtime home: `/var/lib/easy-proxy`
- SQLite DB: `/var/lib/easy-proxy/data/data.db`
- connector work dir: `/var/lib/easy-proxy/connectors`
- bundled connector binary: `/usr/local/bin/ech-workers`
- bundled preferred-IP generator: `/usr/local/bin/cfst`
- bundled cloudflared DNS proxy helper: `/usr/local/bin/cloudflared`

## GHCR Image Contract

The preferred deployment path for `NeuroPlugin` node templates is now a
prebuilt GHCR image instead of runtime binary downloads.

Target image name:

- `ghcr.io/<owner>/easy-proxy-monorepo-service:<release-tag>`

Expected release behavior:

- the image is built from:
  - `deploy/service/base/Dockerfile`
- the Docker build context is the root `EasyProxy` repo
- the image already contains:
  - `easy-proxy`
  - `ech-workers`
  - `cfst`
  - `cloudflared`
- `control_center_worker` should only inject:
  - `EASY_PROXY_CONFIG_PATH`
  - runtime tuning envs such as DNS proxy flags

Local publish helper:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\service\base\scripts\publish-ghcr-easy-proxy-service.ps1 `
  -ReleaseTag release-20260404-001
```

Root one-click wrappers:

```powershell
# publish only the primary EasyProxy service image
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project publish-easyproxy-image `
  -ReleaseTag release-20260427-001

# publish the primary service image plus the optional standalone ech-workers image
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project publish-core-images `
  -ReleaseTag release-20260427-001
```

GitHub Actions workflow:

- `.github/workflows/publish-ghcr-images.yml`
  - tag push:
    - `release-*`
    - `v*`
  - manual dispatch:
    - target `both`, `easyproxy`, or `ech-workers`
    - platforms `linux/amd64` or `linux/amd64,linux/arm64`
  - publishes the service image without requiring a local Docker daemon on the
    operator machine
- `.github/workflows/publish-service-base-config.yml`
  - publishes the rendered runtime config to private R2 storage
  - can also emit an encrypted owner-only import-code artifact

The publish helper now prefers the current machine Docker login state and only
attempts an explicit `docker login` when credentials were supplied.

The root GHCR publish wrapper now fails closed when:

- `config.yaml` is missing and no explicit `-GhcrOwner` was provided
- `ghcr.owner` still contains a placeholder value such as `your-github-owner`

Local Docker API smoke:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\service\base\scripts\smoke-easy-proxy-docker-api.ps1 `
  -KeepArtifacts
```

The smoke script builds the image from `deploy/service/base/Dockerfile`,
launches `easy-proxy-monorepo-service` through Docker Compose with the formal
file-mount contract, and verifies the management API and container runtime
paths.

Published-image smoke path:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\service\base\scripts\smoke-easy-proxy-docker-api.ps1 `
  -Image ghcr.io/<owner>/easy-proxy-monorepo-service:<release-tag>
```

Full live runtime validation:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\service\base\scripts\validate-easy-proxy-runtime.ps1 `
  -Image easyproxy/easy-proxy-monorepo-service:validation-20260426 `
  -KeepArtifacts
```

Repository preflight before any publish:

1. check `git remote -v` in:
   - the monorepo root `EasyProxy`
   - `service/base`
   - `workers/ech-workers-cloudflare` if the worker image or deployment code
     changed
2. check `git branch -vv` and confirm `[origin/main]` exists
3. if a repository has no `origin`, create the GitHub repository first and add:
   - `git remote add origin https://github.com/aiaimimi0920/<repo-name>.git`
4. push the monorepo root after verifying any externally maintained forks or
   companion repositories you still track outside this tree

Operational consequence:

- node-local `easy-proxy-monorepo-service` no longer needs to download `easy-proxy`
  or `ech-workers` from GitHub at container startup
- this keeps the runtime path under monorepo control and makes ECH helper
  changes part of the normal image release flow

## Private Config Distribution

The runtime image now supports bootstrap-driven config loading:

- bootstrap path:
  - `/etc/easy-proxy/bootstrap/r2-bootstrap.json`
- import-code env:
  - `EASY_PROXY_IMPORT_CODE`

When `/etc/easy-proxy/config.yaml` is missing, the entrypoint can bootstrap the
runtime config from private R2 storage before starting `easy-proxy`.

See:

- [service-base-config-distribution.md](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/docs/service-base-config-distribution.md)

## Local Source Management

The local `EasyProxy` runtime already supports both categories the
monorepo deployment needs:

- `代理线路`
  - persistent manual nodes via WebUI or management API
  - CRUD endpoints:
    - `GET /api/nodes/config`
    - `POST /api/nodes/config`
    - `PUT /api/nodes/config/:name`
    - `PATCH /api/nodes/config/:name`
    - `DELETE /api/nodes/config/:name`
- `订阅链接`
  - persistent local subscription URLs stored in config
  - managed through:
    - `GET /api/settings`
    - `PUT /api/settings`
  - relevant field:
    - `subscriptions`
- `本地 connector 源`
  - persistent local connector templates and active connector entries stored in
    config
  - managed through:
    - `GET /api/connectors/config`
    - `POST /api/connectors/config`
    - `PUT /api/connectors/config/:name`
    - `PATCH /api/connectors/config/:name`
    - `DELETE /api/connectors/config/:name`
  - relevant config field:
    - `connectors`

This is separate from remote `MiSub` manifest sync. Local sources remain
runtime-owned by `EasyProxy`; remote manifest sources remain ephemeral.

## Effective Node Queries

Runtime health validation now exists at two layers:

- pool selection
  - runtime traffic already prefers healthy, non-blacklisted upstreams
- management API
  - `GET /api/nodes?only_available=1`
    - returns only nodes that passed the initial health check and are not
      blacklisted
  - `GET /api/nodes?prefer_available=1`
    - returns all nodes, but currently effective nodes are ordered first

The export endpoint remains availability-filtered:

- `GET /api/export`

## Best Proxy Selection

`GET /api/best-proxy` returns the highest-ranked available proxy node(s),
ready for external programs to use directly.

Basic usage:

```bash
# single best node
curl http://localhost:29888/api/best-proxy

# top 5 nodes
curl http://localhost:29888/api/best-proxy?top=5
```

Response format:

```json
{
  "nodes": [
    {
      "name": "US-8|2.2MB S",
      "tag": "us-8-2-2mb-s",
      "proxy_url": "http://0.0.0.0:25126",
      "port": 25126,
      "availability_score": 100,
      "last_latency_ms": 342,
      "active_connections": 0,
      "region": "us"
    }
  ],
  "total_available": 28
}
```

Ranking criteria (in priority order):

1. `availability_score` — higher is better
2. `last_latency_ms` — lower is better (nodes with no latency data rank last)
3. `active_connections` — lower is better

Only nodes that pass the effective filter (initial check done, available, not
blacklisted) are included.

Integration examples:

```bash
# set http_proxy from the best node
export http_proxy=$(curl -s http://localhost:29888/api/best-proxy | jq -r '.nodes[0].proxy_url')

# use directly with curl
curl -x $(curl -s http://localhost:29888/api/best-proxy | jq -r '.nodes[0].proxy_url') https://www.google.com

# Python
import requests, json
best = json.loads(requests.get("http://localhost:29888/api/best-proxy").text)
proxy_url = best["nodes"][0]["proxy_url"]
requests.get("https://www.google.com", proxies={"http": proxy_url, "https": proxy_url})
```

When management password is set, pass it as `Authorization` header:

```bash
curl -H "Authorization: <password>" http://localhost:29888/api/best-proxy
```

## Preferred ECH Entry IP Automation

Preferred ECH entry-IP management now has two modes.

Primary local mode:

- keep an `ech_worker` connector template in local `connectors`
- let `EasyProxy` generate preferred entry-IP connector variants locally
- keep those generated connectors local to the current runtime host
- no write-back to `MiSub` is required for the local-only flow

Management API for the local generation flow:

- `POST /api/connectors/config/:name/preferred-ips/refresh`

Request body example:

```json
{
  "top_count": 5,
  "latency_threads": 200,
  "latency_samples": 4,
  "max_loss_rate": 0,
  "all_ip": false
}
```

Runtime packaging:

- the Docker image now contains:
  - `/usr/local/bin/ech-workers`
  - `/usr/local/bin/cfst`
  - `/usr/local/share/cfst/ip.txt`

Local generator runtime config lives under:

- `source_sync.connector_runtime.preferred_ip.*`

Secondary migration/ops mode:

- the monorepo script below still exists when you intentionally want to
  regenerate preferred connector entries and write them back into a `MiSub`
  profile:

- script:
  - `deploy/service/base/scripts/update_ech_preferred_ips.ps1`

What the script does:

- runs `CloudflareSpeedTest`
- selects the top `N` Cloudflare IPs for the worker entry layer
- generates `kind=connector` + `connector_type=ech_worker` sources
- optionally writes those sources back into the target `MiSub` profile
- saves run artifacts under:
  - `tmp/ech-workers-cloudflare/preferred-ip/<timestamp>`

Important detail:

- for the ECH worker case, the script uses generic Cloudflare `:443` latency
  sorting from `CloudflareSpeedTest`
- it does not rely on worker-root HTTP response codes, because the worker root
  is not a stable `CloudflareSpeedTest` HTTPing target

Typical reuse of an existing result file for a `MiSub`-managed profile:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\service\base\scripts\update_ech_preferred_ips.ps1 `
  -ProfileId easyproxies-ech-runtime `
  -PreferCustomDomain `
  -CustomDomainUrl https://proxyservice-ech-workers.aiaimimi.com:443 `
  -ReuseResultCsvPath .\tmp\ech-workers-cloudflare\preferred-ip\result.csv `
  -ApplyToMiSub
```

Typical full run:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\service\base\scripts\update_ech_preferred_ips.ps1 `
  -ProfileId easyproxies-ech-runtime `
  -PreferCustomDomain `
  -CustomDomainUrl https://proxyservice-ech-workers.aiaimimi.com:443 `
  -TopCount 5 `
  -ApplyToMiSub
```

Useful override flags:

- `-WorkerUrl`
- `-PreferCustomDomain`
- `-CustomDomainUrl`
- `-AccessToken`
- `-MiSubBaseUrl`
- `-AdminPassword`
- `-ManifestToken`
- `-TopCount`
- `-ReuseResultCsvPath`
- `-AllIP`

Recommended custom-domain migration order:

1. Deploy `workers/ech-workers-cloudflare` with the custom-domain route
   `proxyservice-ech-workers.aiaimimi.com`.
2. Validate that both the custom domain and `workers.dev` still complete the
   authenticated WebSocket handshake.
3. Run `update_ech_preferred_ips.ps1` with `-PreferCustomDomain -ApplyToMiSub`
   so the managed `easyproxies-ech-runtime` profile rewrites its connector
   `input/url` to the custom domain while preserving the selected `server_ip`
   values.
4. Rerun `deploy/service/base/scripts/validate-easy-proxy-runtime.ps1`.
5. Only after end-to-end validation should `workers.dev` stop being referenced
   as the primary machine-consumer URL.

## Migration Notes

- The current local `config.yaml` is based on the latest legacy runtime profile
  `node-current-machine-host-standard`.
- Legacy flat keys such as `sub_refresh_interval` were translated to the
  current nested `subscription_refresh.*` structure expected by
  `service/base`.
- Source-sync settings now live under `source_sync.*`.
- Manifest connector execution settings now live under
  `source_sync.connector_runtime.*`.
- The compose service name is `easy-proxy-monorepo-service`, and the
  `easy-proxy://easy-proxy-monorepo-service:29888` integration contract is the
  canonical node-local reference for the monorepo deployment.
