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

## Private Config

Do not commit live deployment values.

Keep:

- secrets under the shared `AIRead` archive
- runtime-local config under ignored files in `deploy/service/base`
- private Cloudflare secrets in platform secret stores rather than committed
  files
