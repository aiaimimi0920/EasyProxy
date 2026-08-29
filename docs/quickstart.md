# Quick Start

## 1. Fork and recursive checkout

Create a GitHub fork, enable Actions in that fork, and use its URL below:

```powershell
git clone --recurse-submodules https://github.com/<OWNER>/<REPOSITORY>.git
Set-Location <REPOSITORY>
git submodule status --recursive
```

For the full first-bootstrap, update, recovery, and release path, follow
[`fork-operator-guide.md`](fork-operator-guide.md).

## 2. Initialize the two configuration layers

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-topology.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\init-runtime-config.ps1
```

Edit `topology.yaml` for deployment/resource choices. Its `secrets` fields are
environment variable names, not values. Edit
`deploy/service/base/config.yaml` for local listener, source, connector,
routing, gateway, and management behavior. Docker mounts that host file as the
writable in-container authority at `/var/lib/easyproxy/config/config.yaml`;
`/etc/easyproxy/config.yaml` is only a first-start migration/bootstrap copy.

Validate deployment configuration:

```powershell
Set-Location tools/easyproxyctl
go test ./...
go run .\cmd\easyproxyctl topology validate --file ..\..\topology.yaml
go run .\cmd\easyproxyctl topology names --file ..\..\topology.yaml
Set-Location ..\..
```

The local runtime config is initialized only when missing. Neither topology
validation nor an ordinary deploy overwrites subsequent WebUI/API changes.

## 3. Resolve secret references

Set the environment variables named in `topology.yaml`. A minimal cloud setup
normally includes:

```powershell
$env:CLOUDFLARE_ACCOUNT_ID = '<account-id>'
$env:CLOUDFLARE_API_TOKEN = '<least-privilege-token>'
$env:MISUB_ADMIN_PASSWORD = '<strong-password>'
$env:MISUB_COOKIE_SECRET = '<stable-random-secret>'
$env:MISUB_MANIFEST_TOKEN = '<machine-token>'
$env:MISUB_CRON_SECRET = '<cron-token>'
$env:EASYPROXY_BACKUP_PASSPHRASE = '<independent-high-entropy-passphrase>'
$env:ECH_TOKEN = '<ech-token>'
$env:R2_ACCESS_KEY_ID = '<r2-key-id>'
$env:R2_SECRET_ACCESS_KEY = '<r2-secret>'
```

For GitHub-hosted deployment, store ordinary deployment credentials as Actions
repository secrets and account/zone IDs as repository variables. Restore and
ECH rotation use separately protected GitHub Environments. See
[`github-secrets.md`](github-secrets.md) and
[`secrets-and-permissions.md`](secrets-and-permissions.md).

Do not replace `ECH_TOKEN` as an update shortcut. Configure `ECH_TOKEN_NEXT`
only when running the protected rotation workflow described in
[`ech-lifecycle.md`](ech-lifecycle.md).

## 4. Deploy the local runtime

Build from this checkout:

```powershell
.\scripts\deploy-subproject.ps1 -Project easyproxy -TopologyPath .\topology.yaml
```

Or pull a published image:

```powershell
.\scripts\deploy-subproject.ps1 `
  -Project easyproxy-ghcr `
  -TopologyPath .\topology.yaml `
  -GhcrOwner <owner> `
  -ReleaseTag <tag>
```

For a standalone launcher:

```powershell
pwsh .\deploy-host.ps1 -Project easyproxy-ghcr -GhcrOwner <owner> -ReleaseTag <tag>
```

`deploy-host.ps1` recursively clones all public submodules and keeps
`topology.yaml` plus `easyproxy-runtime.yaml` outside the replaceable source
cache.

## 5. Deploy cloud components

The commands below are local wrappers. A new fork should normally use the
ordered GitHub Actions bootstrap in
[`fork-operator-guide.md`](fork-operator-guide.md), which also covers the R2
prerequisite and recovery artifacts.

```powershell
# MiSub Pages
.\scripts\deploy-subproject.ps1 -Project misub-pages -TopologyPath .\topology.yaml

# ECH Worker dry-run, then deploy
.\scripts\deploy-subproject.ps1 -Project ech-workers-cloudflare `
  -TopologyPath .\topology.yaml -DryRun
.\scripts\deploy-subproject.ps1 -Project ech-workers-cloudflare `
  -TopologyPath .\topology.yaml

# Aggregator GitHub workflow
.\scripts\deploy-subproject.ps1 -Project aggregator -TopologyPath .\topology.yaml
```

The local MiSub Docker compatibility path uses
`upstreams/misub/.env`; copy `.env.example` and keep that file untracked.

## 6. Trusted-LAN access

In the local runtime config, use `mode: pool`, `listener.protocol: mixed`, and
`local_server.enabled: true`, then set a strong canonical username/password.
Restrict `22323` and `29888` to trusted LAN CIDRs. See
[`local-server.md`](local-server.md) before exposing the service to other
devices.

If `management.password` is empty, EasyProxy accepts only a loopback management
listener. A wildcard, LAN, or other non-loopback management address requires a
password; Local Server mode derives it from `local_server.auth`.

## 7. Validate

```powershell
# Root contracts and scripts
python -m pytest -q tests

# Lifecycle CLI
Set-Location tools/easyproxyctl
go test -count=1 ./...
go vet ./...
Set-Location ..\..

# Runtime
Set-Location service/base
go test -count=1 ./...
```

Pull requests call `.github/workflows/reusable-validate.yml` through
`.github/workflows/validate.yml`. That path is read-only and receives no
production secrets.
