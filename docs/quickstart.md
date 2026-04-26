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
powershell -ExecutionPolicy Bypass -File .\scripts\init-config.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\render-derived-configs.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\build-easyproxy-image.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\build-ech-workers-image.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-misub.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-ech-workers-cloudflare.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-aggregator.ps1
```

Notes:

- root scripts now read defaults from `config.yaml`
- `deploy-misub.ps1` defaults to Cloudflare Pages mode. Use `-Mode docker`
  if you explicitly want the Docker/VPS path.
- `deploy-aggregator.ps1` targets the current GitHub Actions + R2 deployment
  model rather than a local runtime.

## Private Config

Do not commit live deployment values.

Keep:

- secrets under the shared `AIRead` archive
- runtime-local config under ignored files in `deploy/service/base`
- private Cloudflare secrets in platform secret stores rather than committed
  files
