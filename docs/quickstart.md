# Quick Start

## Main Runtime

Build the frontend:

```powershell
Set-Location service/base/frontend
npm ci
npm run build
```

Build the Go runtime:

```powershell
Set-Location service/base
go mod download
go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" -o easy-proxy ./cmd/easy_proxies
```

## MiSub

```powershell
Set-Location upstreams/misub
npm install
npm run build
```

## Deployment Assets

The initial monorepo migration keeps module-local deployment contracts intact.

Read:

- `deploy/service/base/README.md`
- `deploy/upstreams/misub/README.md`
- `deploy/upstreams/aggregator/README.md`
- `deploy/workers/ech-workers-cloudflare/README.md`

## Root Config

Initialize the operator config:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-config.ps1
```

Then render the module-specific config files from the root `config.yaml`:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\render-derived-configs.ps1
```

This renders:

- `deploy/service/base/config.yaml`
- `upstreams/misub/.env`
- `workers/ech-workers-cloudflare/.dev.vars`

## Root Operator Entry Points

Run these from the repository root:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project easyproxy -InitConfig
powershell -ExecutionPolicy Bypass -File .\scripts\init-config.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\render-derived-configs.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-easyproxy.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\build-easyproxy-image.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\build-ech-workers-image.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-misub.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-ech-workers-cloudflare.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-aggregator.ps1
```

Notes:

- `deploy-subproject.ps1` is the recommended one-click entrypoint for
  per-module deploy/build tasks. It can initialize `config.yaml` automatically
  with `-InitConfig`.
- root scripts now read defaults from `config.yaml`
- `deploy-easyproxy.ps1` deploys `service/base` through the monorepo Docker
  Compose contract and renders `deploy/service/base/config.yaml` first
- `deploy-misub.ps1` defaults to Cloudflare Pages mode. Use `-Mode docker`
  if you explicitly want the Docker/VPS path.
- `deploy-aggregator.ps1` targets the current GitHub Actions + R2 deployment
  model rather than a local runtime.

One-click examples:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project easyproxy -InitConfig
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project misub-pages -InitConfig
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project misub-docker -InitConfig
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project ech-workers-cloudflare -InitConfig
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project aggregator -InitConfig
```

## Validation

Run these checks before publishing images or merging risky runtime changes:

```powershell
# Root script smoke coverage
python -m unittest discover -s "tests" -p "test_*.py" -v

# Aggregator regression coverage
python -m unittest discover -s "upstreams/aggregator/tests" -p "test_*.py" -v

# service/base critical Go packages
Set-Location service/base
go test ./internal/monitor
go test ./internal/boxmgr
go test ./internal/config
```

GitHub Actions equivalents:

- `.github/workflows/validate.yml`
- `.github/workflows/publish-ghcr-images.yml`
  - GHCR publish now depends on the validation preflight job

## Private Config

Do not commit live deployment values.

Keep:

- secrets under the shared `AIRead` archive
- runtime-local config under ignored files in `deploy/service/base`
- private Cloudflare secrets in platform secret stores rather than committed
  files
