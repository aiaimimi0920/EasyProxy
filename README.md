# EasyProxy

EasyProxy is a monorepo for building a personal proxy topology from public
aggregation, a shared MiSub registry, an ECH Worker, and one LAN-local proxy
service. Cloud components are deployed once per account; a local EasyProxy
instance can serve an entire trusted LAN.

## Architecture

| Layer | Component | Role |
| --- | --- | --- |
| Cloud collection | `upstreams/aggregator` | Produces fallback subscription artifacts from public sources. |
| Cloud registry | `upstreams/misub` | Stores subscriptions, direct proxies, connectors, profiles, and machine manifests. |
| Cloud connector | `workers/ech-workers-cloudflare` | Provides the Cloudflare-side ECH entrypoint. |
| Local execution | `service/base` | Merges sources, runs connectors, probes nodes, exposes proxy listeners, and provides the management API/WebUI. |
| Local helper | `upstreams/ech-workers` | Runs ECH connectors requested by the local service. |

The three `upstreams/*` directories are public Git submodules pinned to commits
in maintained forks. First-party integration, deployment, workflows, local
runtime, and Cloudflare Worker code are owned by this repository.

## Clone

Fork this repository first, then clone the fork recursively. Aggregator contains
a nested public submodule, so a non-recursive checkout is incomplete:

```powershell
git clone --recurse-submodules https://github.com/<OWNER>/<REPOSITORY>.git
Set-Location <REPOSITORY>
git submodule status --recursive
```

Repair an existing checkout with:

```powershell
git submodule sync --recursive
git submodule update --init --recursive
```

The complete GitHub, Cloudflare, recovery, publication, and local installation
path for a new fork is [`docs/fork-operator-guide.md`](docs/fork-operator-guide.md).

## Configuration Authority

EasyProxy deliberately separates deployment configuration from local runtime
configuration.

### `topology.yaml`

`topology.yaml` is the non-secret deployment contract. It selects components,
Cloudflare resource names, schedules, profiles, local install/access modes, and
release channels. Secret fields contain environment variable **names**, never
secret values.

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-topology.ps1
Set-Location tools/easyproxyctl
go run .\cmd\easyproxyctl topology validate --file ..\..\topology.yaml
go run .\cmd\easyproxyctl topology names --file ..\..\topology.yaml
Set-Location ..\..
```

The schema is [`topology.schema.json`](topology.schema.json). The full ownership
contract is in [`docs/topology.md`](docs/topology.md).

### Local runtime config

`deploy/service/base/config.yaml` is the EasyProxy runtime authority. It owns
listeners, management access, source sync, subscriptions, connectors, routing,
gateway/TUN, refresh, and probing behavior. Initialize it once:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-runtime-config.ps1
```

Ordinary deploys and topology updates never overwrite an existing runtime
config. WebUI/API changes persist to that same deployed file. The former root
`config.yaml` renderer and GitHub-setting synchronizer were removed so there is
no second writer.

### Secrets

Resolve every reference named by `topology.yaml` in the local process
environment or a protected GitHub Environment. For example:

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

Do not commit those values. See
[`docs/secrets-and-permissions.md`](docs/secrets-and-permissions.md).
Ordinary ECH deployment preserves `ECH_TOKEN`; use the protected
[`Rotate ECH Token`](docs/ech-lifecycle.md) workflow for a dual-token rotation.

## Local EasyProxy Deployment

Prerequisites:

- Windows PowerShell 5.1 or PowerShell 7;
- Docker with `docker compose`;
- `easyproxyctl`, or Go 1.24+ while running from a source checkout.

Build and start from source:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project easyproxy `
  -TopologyPath .\topology.yaml
```

Deploy a published GHCR image:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project easyproxy-ghcr `
  -TopologyPath .\topology.yaml `
  -GhcrOwner <owner> `
  -ReleaseTag <release-tag>
```

Use the standalone host bootstrap when only `deploy-host.ps1` is present. It
performs a recursive Git clone into a cache and keeps topology/runtime files
outside the replaceable code checkout:

```powershell
pwsh .\deploy-host.ps1 `
  -Project easyproxy-ghcr `
  -GhcrOwner <owner> `
  -ReleaseTag <release-tag>
```

For side-by-side validation, override container and host port bindings:

```powershell
pwsh .\deploy-host.ps1 `
  -Project easyproxy `
  -ContainerName easy-proxy-candidate `
  -PoolPortBinding 22324:22323 `
  -ManagementPortBinding 29889:29888 `
  -NetworkAlias easy-proxy-candidate `
  -ComposeProjectName easy-proxy-candidate
```

Published tags also contain native archives for Linux amd64/arm64 and Windows
amd64, matching `easyproxyctl` packages, `SHA256SUMS`, and
`release-manifest.json`. Installation and rollback commands are documented in
[`docs/native-install-update.md`](docs/native-install-update.md). Windows arm64
is explicitly unsupported; Windows native mode does not provide transparent
gateway ingress.

For a Linux NAS with Docker Compose, use the pinned-image deployment and
preflight checks in [`deploy/nas`](deploy/nas/README.md). The NAS path preserves
`runtime/config.yaml` and `runtime/data` as host bind mounts and rejects
`latest`.

## Cloud And Publish Entry Points

The PowerShell dispatcher supports:

- `easyproxy`, `easyproxy-ghcr`;
- `misub-pages`, `misub-docker`;
- `aggregator`, `ech-workers-cloudflare`;
- `build-easyproxy-image`, `build-ech-workers-image`;
- `publish-easyproxy-image`, `publish-ech-workers-image`,
  `publish-core-images`;
- `publish-service-base-config` for an explicit local runtime snapshot.

Examples:

```powershell
# MiSub Pages, using deterministic topology naming
.\scripts\deploy-subproject.ps1 -Project misub-pages -TopologyPath .\topology.yaml

# ECH Worker dry-run
.\scripts\deploy-subproject.ps1 -Project ech-workers-cloudflare `
  -TopologyPath .\topology.yaml -DryRun

# Trigger Aggregator workflow
.\scripts\deploy-subproject.ps1 -Project aggregator -TopologyPath .\topology.yaml

# Publish both local images
.\scripts\deploy-subproject.ps1 -Project publish-core-images `
  -TopologyPath .\topology.yaml -GhcrOwner <owner> -ReleaseTag <tag>
```

GitHub Actions currently exposes validation, Aggregator deployment, Cloudflare
deployment, multi-architecture GHCR publication, and native GitHub Release
publication. The PR validation
entry delegates to `.github/workflows/reusable-validate.yml`; it is read-only
and receives no production secrets.

Fork operators should follow the ordered workflow inputs and provider
prerequisites in [`docs/fork-operator-guide.md`](docs/fork-operator-guide.md)
rather than reusing maintainer resource names or URLs.

Aggregator uses candidate, immutable release, stable-manifest, and
last-known-good layers. Fork configuration and failure recovery are documented
in [`docs/aggregator-publication.md`](docs/aggregator-publication.md).

## `easyproxyctl`

The first-party lifecycle CLI lives in `tools/easyproxyctl` and is shared by
scripts and Actions.

```text
easyproxyctl topology validate --file topology.yaml
easyproxyctl topology show --file topology.yaml
easyproxyctl topology names --file topology.yaml
easyproxyctl manifest build --topology topology.yaml --output deployment-manifest.json
easyproxyctl manifest verify --file deployment-manifest.json
```

Cloud and local lifecycle commands resolve exact provider identities and fail
closed on missing, ambiguous, or mismatched resources; the CLI never reports a
fake deployment success.

## Runtime Access

Default ports in the deployment template:

| Port | Purpose |
| --- | --- |
| `22323` | HTTP/SOCKS mixed proxy listener when configured accordingly |
| `29888` | Management API and embedded WebUI |
| `25000+` | Optional multi-port listener range |

Local Server is the preferred trusted-LAN mode. When enabled, use `mode: pool`,
`listener.protocol: mixed`, one strong canonical credential, and firewall rules
that restrict proxy/management ports to trusted CIDRs. See
[`docs/local-server.md`](docs/local-server.md).

## Development And Validation

```powershell
# Lifecycle CLI
Set-Location tools/easyproxyctl
go test -count=1 ./...
go vet ./...
Set-Location ..\..

# Root scripts
python -m unittest discover -s tests -p "test_*.py" -v

# Aggregator fork
python -m unittest discover -s upstreams/aggregator/tests -p "test_*.py" -v

# MiSub fork
Set-Location upstreams/misub
npm ci
npm run test:run
Set-Location ..\..

# Local runtime
Set-Location service/base
go test -count=1 ./...
Set-Location frontend
npm ci
npm run test
npm run lint
npm run build
```

See [`docs/DEVELOPMENT_STANDARD.md`](docs/DEVELOPMENT_STANDARD.md) for ownership,
size, test, security, and submodule rules.

## Repository Map

```text
service/base/                    local runtime, API, and WebUI
upstreams/aggregator/            maintained Aggregator fork submodule
upstreams/misub/                 maintained MiSub fork submodule
upstreams/ech-workers/           maintained ECH helper fork submodule
workers/ech-workers-cloudflare/  first-party Cloudflare Worker
deploy/                          runtime and provider packaging
scripts/                         thin operator wrappers
tools/easyproxyctl/              lifecycle contract implementation
tests/                           root integration and script tests
docs/                            architecture and operations contracts
```

Private credentials, operator notes, live state, downloaded bootstrap files,
and generated runtime configs must remain untracked.
