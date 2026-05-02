# EasyProxy

EasyProxy is the public monorepo entrypoint for the EasyProxy stack.

It replaces the older multi-repository source workspace with a single
contributor-facing repository while preserving explicit boundaries between the
main runtime, the shared manifest registry, upstream-tracked modules, and
deployment assets.

This repository intentionally avoids root-level submodules. External
contributors only need one repository and one pull request target.

## Development Workflow

See `docs/development-workflow.md` for the shared cross-repository development
rules used for local-first iteration, temporary test assets, and final
GHCR-based validation.

## What Ships

The public repository now owns the full operator surface:

- native `aggregator` publishing to `https://sub.aiaimimi.com`
- native `MiSub` deployment to Cloudflare Pages
- native `ech-workers-cloudflare` deployment to Cloudflare Workers
- GHCR publishing for:
  - `service/base`
  - local `ech-workers`
- private `service/base` config distribution through Cloudflare R2
- post-deploy verification for every publish/deploy workflow

This is the same operating model we want external users and maintainers to see:
one repository, one release surface, one CI/CD control plane for cloud and
publish tasks, while local runtime deployment stays script-driven.

## Shared Config

Copy `config.example.yaml` to `config.yaml` before using the root operator
scripts.

The root `config.yaml` is the single operator-facing config entrypoint for the
monorepo. It collects:

- `serviceBase`
  - EasyProxy image/build metadata and runtime config overlay
- `misub`
  - Cloudflare Pages defaults and Docker `.env` values
- `aggregator`
  - native publish workflow inputs and public artifact metadata
- `ghcr`
  - GHCR owner and published image names for the reusable container releases
- `echWorkers`
  - standalone local image build metadata
- `echWorkersCloudflare`
  - Wrangler deploy metadata and local secret values
- `distribution`
  - private service/base runtime config distribution metadata

Use `scripts/render-derived-configs.ps1` to generate module-specific files such
as:

- `deploy/service/base/config.yaml`
- `upstreams/misub/.env`
- `workers/ech-workers-cloudflare/.dev.vars`

## Repository Layout

```text
service/
  base/
upstreams/
  misub/
  aggregator/
  ech-workers/
workers/
  ech-workers-cloudflare/
deploy/
  service/
    base/
  upstreams/
    misub/
    aggregator/
    ech-workers/
  workers/
    ech-workers-cloudflare/
docs/
scripts/
api/
```

## Module Roles

### `service/base`

The main EasyProxy runtime.

Responsibilities:

- local proxy runtime and management API
- source merge logic for local, manifest, and fallback inputs
- local connector execution for supported connector sources such as `ech_worker` and `zenproxy_client`
- node scoring, health checks, and export surfaces

### `upstreams/misub`

The upstream-tracked shared source registry and manifest center that powers
`service/base`.

Responsibilities:

- source registry for `subscription`, `proxy_uri`, and `connector`, including provider-style connectors such as `ech_worker` and `zenproxy_client`
- machine manifest endpoint for `service/base`
- Cloudflare Pages + D1 primary deployment path
- Docker / VPS compatibility path

### `upstreams/aggregator`

The upstream-tracked fallback artifact producer.

Responsibilities:

- crawler and batch aggregation inputs
- published fallback subscription artifacts
- upstream sync boundary kept narrow and reviewable

### `upstreams/ech-workers`

The upstream-tracked local ECH connector helper.

Responsibilities:

- local helper runtime used by `service/base` connector execution
- upstream sync boundary for local ECH helper logic

### `workers/ech-workers-cloudflare`

The self-owned Cloudflare-side ECH entrypoint worker.

Responsibilities:

- public Worker endpoint for managed ECH connector profiles
- Cloudflare deployment code owned directly in this monorepo

## Quick Start

### Root host deploy

```powershell
pwsh .\deploy-host.ps1
```

For a side-by-side isolated host deploy on a machine that already has a live
EasyProxy runtime, override the container name and host port bindings:

```powershell
pwsh .\deploy-host.ps1 `
  -Project easyproxy `
  -ContainerName easy-proxy-monorepo-service-blankfinal01 `
  -PoolPortBinding 22324:22323 `
  -ManagementPortBinding 29889:29888 `
  -MultiPortBinding 26000-26500:25000-25500 `
  -NetworkAlias easy-proxy-service-blankfinal01
```

### EasyProxy runtime (manual local build)

```powershell
Set-Location service/base/frontend
npm ci
npm run build

Set-Location ..
go mod download
go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" -o easy-proxy ./cmd/easy_proxies
```

### MiSub runtime

```powershell
Set-Location upstreams/misub
npm install
npm run build
```

### Deployment Assets

Read the module-specific deployment notes:

- `deploy/service/base`
- `deploy/upstreams/misub`
- `deploy/upstreams/aggregator`
- `deploy/workers/ech-workers-cloudflare`

## Documentation

- `docs/architecture.md`
- `docs/quickstart.md`
- `docs/release-checklist.md`
- `docs/release-notes-template.md`
- `docs/service-base-config-distribution.md`
- `docs/unified-source-architecture.md`
- `docs/upstream-sync.md`
- `docs/migration-plan.md`
- `docs/root-host-deploy-standard.md`
- `CONTRIBUTING.md`

## Operator Scripts

Root-level operator entrypoints live under `scripts/`:

- `deploy-host.ps1`
  - single-file host deploy wrapper
  - can be downloaded and run without manually checking out the full repository
  - bootstraps a local repo cache automatically before invoking the canonical deploy path
- `scripts/deploy-subproject.ps1`
  - one-click entrypoint for per-module deploy/build tasks
  - auto-initializes `config.yaml` from template with `-InitConfig`
- `scripts/init-config.ps1`
  - copies `config.example.yaml` to `config.yaml`
- `scripts/render-derived-configs.ps1`
  - renders module-specific config files from the root `config.yaml`
- `scripts/sync-github-deployment-settings.ps1`
  - regenerates the local ignored `config.yaml` and synchronizes GitHub
    deployment secrets / variables from the current operator state

- `scripts/deploy-easyproxy.ps1`
  - renders and deploys `service/base` through Docker Compose
  - can also pull a published GHCR image when passed `-FromGhcr` plus
    `-ReleaseTag` or `-Image`
- `scripts/deploy-aggregator.ps1`
  - triggers the native aggregator deployment workflow in the current repository
- `scripts/deploy-misub.ps1`
  - deploys `upstreams/misub` either to Cloudflare Pages or through Docker
- `scripts/deploy-ech-workers-cloudflare.ps1`
  - deploys the Cloudflare Worker in `workers/ech-workers-cloudflare`
- `scripts/build-easyproxy-image.ps1`
  - builds the local EasyProxy monorepo image
- `scripts/build-ech-workers-image.ps1`
  - builds a standalone local image for `upstreams/ech-workers`
- `scripts/publish-ghcr-images.ps1`
  - publishes the primary EasyProxy service image, the standalone
    `ech-workers` image, or both to GHCR
- `scripts/publish-service-base-config.ps1`
  - uploads the rendered `service/base` runtime config to private R2 storage
    and writes the current service distribution manifest

GitHub-hosted cloud and publish workflows:

- `.github/workflows/publish-ghcr-images.yml`
  - publishes GHCR images on tag push or manual workflow dispatch
  - does not require local Docker on the operator machine
- `.github/workflows/publish-service-base-config.yml`
  - publishes the `service/base` runtime config distribution manifest and
    optional encrypted import-code artifact
- `.github/workflows/publish-github-release.yml`
  - creates or updates the GitHub Release body from the current EasyProxy
    release metadata
- `.github/workflows/deploy-cloudflare.yml`
  - deploys MiSub Pages and `ech-workers-cloudflare` from GitHub-hosted runners
  - supports `bootstrap` and `update` deployment modes with post-deploy verification
- `.github/workflows/deploy-aggregator.yml`
  - runs the native aggregator publish flow from this repository with artifact verification

## Release Surface

The repository now exposes six primary GitHub-hosted operational workflows:

- `Validate`
  - repository regression gate for scripts, Go runtime, and aggregator tests
- `Deploy Aggregator`
  - native crawler publish into the public R2-backed artifact surface
- `Deploy Cloudflare Apps`
  - MiSub Pages + `ech-workers-cloudflare`
- `Publish GHCR Images`
  - `service/base` + local `ech-workers`
- `Publish Service Base Config`
  - private config distribution manifest + optional encrypted import-code artifact
- `Publish GitHub Release`
  - release body automation for tags and manual draft releases

### One-Click Deploy Examples

Run from repository root:

```powershell
# EasyProxy runtime deploy from a single-file host wrapper
powershell -ExecutionPolicy Bypass -File .\deploy-host.ps1 -ReleaseTag release-20260502-001

# EasyProxy runtime deploy (Docker Compose)
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project easyproxy -InitConfig

# EasyProxy runtime deploy from a published GHCR image
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project easyproxy-ghcr -InitConfig -ReleaseTag release-20260502-001

# MiSub Pages deploy
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project misub-pages -InitConfig

# MiSub Docker deploy
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project misub-docker -InitConfig

# Cloudflare worker deploy (with dry-run support)
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project ech-workers-cloudflare -InitConfig -DryRun

# Aggregator workflow deploy
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project aggregator -InitConfig

# Regenerate local config.yaml and sync GitHub deployment settings
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project sync-github-settings -InitConfig

# Publish the primary EasyProxy GHCR image
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project publish-easyproxy-image -ReleaseTag release-20260427-001

# Publish the standalone ech-workers GHCR image
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project publish-ech-workers-image -ReleaseTag release-20260427-001

# Publish both core images with one command
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project publish-core-images -ReleaseTag release-20260427-001

# Publish the private service/base runtime config distribution
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project publish-service-base-config -ReleaseTag release-20260428-001
```

### GitHub Actions Publish

Without local Docker, you can publish from GitHub Actions in two ways:

1. Push a tag named like `release-20260427-001` or `v1.0.0`.
2. Open `Actions -> Publish GHCR Images -> Run workflow`, then choose:
   - `both`
   - `easyproxy`
   - `ech-workers`
   - `linux/amd64`
   - `linux/amd64,linux/arm64`

The workflow publishes to:

- `ghcr.io/<repository-owner>/easy-proxy-monorepo-service:<release-tag>`
- `ghcr.io/<repository-owner>/ech-workers-monorepo:<release-tag>`

The canonical operator baseline is:

- GitHub-hosted cloud deploys plus GHCR/config/release publication
- local script-driven runtime deployment on the target host

### Local EasyProxy Docker Deploy

Use this flow when a user clones the repository onto a target host and wants a
single local script to pull the published GHCR image and deploy
`easy-proxy-monorepo-service` into Docker.

Prerequisites:

- Windows PowerShell
- Docker Desktop or another Docker engine with `docker compose`
- Python 3 with `PyYAML`
- repository checkout on the target host

Prepare the local operator config:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-config.ps1
```

Then edit `config.yaml` and fill in the fields the local runtime actually
needs. At minimum:

- `ghcr.owner`
  - the GitHub owner or org that publishes
    `ghcr.io/<owner>/easy-proxy-monorepo-service:<release-tag>`
- `serviceBase.runtime.source_sync.manifest_url`
  - your real MiSub manifest URL
- `serviceBase.runtime.source_sync.manifest_token`
  - if your manifest endpoint requires auth
- `serviceBase.runtime.management.password`
  - recommended for the management API on `29888`
- any connector secrets you actually use, for example:
  - `serviceBase.runtime.connectors[*].connector_config.api_key`

Recommended local GHCR rollout command:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-easyproxy.ps1 `
  -FromGhcr `
  -ReleaseTag release-20260502-001
```

Equivalent root one-click wrapper:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project easyproxy-ghcr `
  -InitConfig `
  -ReleaseTag release-20260502-001
```

You can also pin the full image directly instead of using `ghcr.owner` plus
`-ReleaseTag`:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-easyproxy.ps1 `
  -FromGhcr `
  -Image ghcr.io/<owner>/easy-proxy-monorepo-service:<release-tag>
```

What the root script does:

- renders `deploy/service/base/config.yaml` from the root `config.yaml`
- ensures the target Docker network exists
- pulls the requested GHCR image unless `-SkipPull` was passed
- writes the runtime `.env` and compose inputs under `deploy/service/base`
- replaces the existing `easy-proxy-monorepo-service` container if one already exists
- runs Docker Compose to bring the runtime back up

Recommended post-deploy checks:

```powershell
docker ps --filter "name=easy-proxy-monorepo-service"

curl.exe -I -x "http://127.0.0.1:22323" "https://www.google.com/generate_204" --max-time 25 -k
```

If you set `serviceBase.runtime.management.password`, you can also inspect the
runtime management API:

```powershell
$headers = @{ Authorization = "<management-password>" }
Invoke-RestMethod -Uri "http://127.0.0.1:29888/api/source-sync/status" -Headers $headers
```

For local GHCR rollout, the canonical root entrypoint is:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-easyproxy.ps1 `
  -FromGhcr `
  -ReleaseTag release-20260502-001
```

### Import Code And Bootstrap Examples

Generate an owner keypair once:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\generate-import-code-keypair.ps1 `
  -PublicKeyOutput .\tmp\easyproxy_import_code_owner_public.txt `
  -PrivateKeyOutput .\tmp\easyproxy_import_code_owner_private.txt `
  -BundleOutput .\tmp\easyproxy_import_code_owner_keypair.json
```

Decrypt an encrypted artifact emitted by `Publish Service Base Config`:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\decrypt-import-code.ps1 `
  -EncryptedFilePath .\service-base-import-code.encrypted.json `
  -PrivateKeyPath .\tmp\easyproxy_import_code_owner_private.txt `
  -OutputPath .\tmp\service-base-import-code.decrypted.json
```

Write a bootstrap JSON from an import code:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\write-service-base-r2-bootstrap.ps1 `
  -ImportCode "<easyproxy-import-v1...>" `
  -OutputPath .\deploy\service\base\bootstrap\r2-bootstrap.json
```

Run the released `service/base` image with an import code:

```powershell
docker run --rm `
  -p 29888:29888 `
  -e EASY_PROXY_IMPORT_CODE="<easyproxy-import-v1...>" `
  ghcr.io/<repository-owner>/easy-proxy-monorepo-service:<release-tag>
```

Full details live in
[service-base-config-distribution.md](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/docs/service-base-config-distribution.md).

For local PowerShell publishing, set `ghcr.owner` in [config.example.yaml](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/config.example.yaml) after copying it to `config.yaml`, or pass `-GhcrOwner` explicitly. The local publish script now fails closed when the config file is missing or the owner still uses a placeholder value.

Supported `-Project` values:

- `easyproxy`
- `easyproxy-ghcr`
- `misub-pages`
- `misub-docker`
- `aggregator`
- `sync-github-settings`
- `ech-workers-cloudflare`
- `build-easyproxy-image`
- `build-ech-workers-image`
- `publish-service-base-config`
- `publish-easyproxy-image`
- `publish-ech-workers-image`
- `publish-core-images`

## Validation Matrix

Local validation commands used by this repository:

```powershell
# Root script smoke tests
python -m unittest discover -s "tests" -p "test_*.py" -v

# Aggregator regression tests
python -m unittest discover -s "upstreams/aggregator/tests" -p "test_*.py" -v

# service/base critical Go regression packages
Set-Location service/base
go test ./internal/monitor
go test ./internal/boxmgr
go test ./internal/config
go test ./internal/subscription
```

Repository CI coverage:

- `.github/workflows/validate.yml`
  - root PowerShell script smoke tests
  - `upstreams/aggregator` regression tests
- `service/base` monitor / boxmgr / config / subscription Go tests
- `.github/workflows/publish-ghcr-images.yml`
  - now runs the validation preflight before publishing GHCR images
- `.github/workflows/deploy-cloudflare.yml`
  - now runs the same validation preflight before deploying MiSub Pages or `ech-workers-cloudflare`
- `.github/workflows/deploy-aggregator.yml`
  - now runs the same validation preflight before running the native aggregator publish flow
- `.github/workflows/publish-service-base-config.yml`
  - now runs the same validation preflight before uploading private service/base runtime config artifacts

## GitHub Secrets

Critical deployment secrets should live in GitHub repository secrets, not in
committed operator files. See
[docs/github-secrets.md](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/docs/github-secrets.md)
for the current secret matrix covering:

- Cloudflare deployment credentials
- MiSub runtime secrets
- `ECH_TOKEN` for `ech-workers-cloudflare`
- the native aggregator secrets and verification variables used by this repository
- the private R2 distribution secrets used by `service/base`

## Release Checklist

Before publishing a public release:

1. Confirm `config.example.yaml` still contains placeholders only, and no real secrets were introduced.
2. Run the local validation matrix or confirm `.github/workflows/validate.yml` passed on the target commit.
3. Confirm the embedded frontend assets in `service/base/internal/monitor/assets` match the current frontend source when WebUI code changed.
4. Confirm GHCR owner/image names are correct for the target repository or organization.
5. If `upstreams/*` changed, note whether each change is an upstream sync import or a local carried patch.
6. If deploy behavior changed, update the corresponding `deploy/*/README.md` notes.
7. Publish via tag push or GitHub Actions only after validation is green.

For release body drafting, start from
[release-notes-template.md](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/docs/release-notes-template.md).

## Private Operator Material

Private deployment notes, secrets, and runtime state do not belong in this
repository.

Local operator material should continue to live under the shared `AIRead`
knowledge base and remain ignored by Git when linked into this repository.
