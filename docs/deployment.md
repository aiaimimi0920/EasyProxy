# EasyProxy 部署、安装与恢复

## 适用范围

This guide covers local quick start, an independent cloud topology, native
installers, the root host entrypoint, and optional private configuration snapshots.
Use your own resource identities and verify the target environment before acting.
Commands that publish, change permissions, restore data, rotate secrets, or remove
resources require the relevant operator authorization; this guide is not approval.

## 目录

- [Quick start](#quickstart)
- [Cloud bootstrap, update, and recovery](#fork-operator-guide)
- [Native installation and rollback](#native-install-update)
- [Root host entrypoint contract](#root-host-deploy-standard)
- [Private configuration distribution](#service-base-config-distribution)

<a id="quickstart"></a>

## Quick Start

### 1. Fork and recursive checkout

Create a GitHub fork, enable Actions in that fork, and use its URL below:

```powershell
git clone --recurse-submodules https://github.com/<OWNER>/<REPOSITORY>.git
Set-Location <REPOSITORY>
git submodule status --recursive
```

Cloud bootstrap, update, recovery, and release acceptance are covered in
[Cloud operations](#fork-operator-guide) below.

### 2. Initialize the two configuration layers

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

### 3. Resolve secret references

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
[`security.md#github-secrets`](security.md#github-secrets) and
[`security.md#secrets-and-permissions`](security.md#secrets-and-permissions).

Do not replace `ECH_TOKEN` as an update shortcut. Configure `ECH_TOKEN_NEXT`
only when running the protected rotation workflow described in
[`cloud-operations.md#ech-lifecycle`](cloud-operations.md#ech-lifecycle).

### 4. Deploy the local runtime

Use the repository-root launcher first. In the checkout initialized above, explicitly select the runtime config you edited:

```powershell
pwsh .\deploy-host.ps1 -Project easyproxy-ghcr -GhcrOwner <owner> -ReleaseTag <tag> -RuntimeConfigPath .\deploy\service\base\config.yaml
```

When downloaded alone, the launcher defaults to `easyproxy-runtime.yaml` beside itself; initialize and edit that file instead. The checkout command above deliberately selects `deploy/service/base/config.yaml` to avoid creating a second configuration authority.

`deploy-host.ps1` recursively clones all public submodules and keeps
`topology.yaml` plus `easyproxy-runtime.yaml` outside the replaceable source
cache. See [the host entrypoint contract](#root-host-deploy-standard).

For developers who already have a complete checkout, the lower-level adapter
can build locally or deploy a specified published image:

```powershell
.\scripts\deploy-subproject.ps1 -Project easyproxy -TopologyPath .\topology.yaml
.\scripts\deploy-subproject.ps1 `
  -Project easyproxy-ghcr `
  -TopologyPath .\topology.yaml `
  -GhcrOwner <owner> `
  -ReleaseTag <tag>
```

### 5. Deploy cloud components

The commands below are local wrappers. A new fork should normally use the
ordered GitHub Actions bootstrap in
[`deployment.md#fork-operator-guide`](#fork-operator-guide), which also covers the R2
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

### 6. Trusted-LAN access

In the local runtime config, use `mode: pool`, `listener.protocol: mixed`, and
`local_server.enabled: true`, then set a strong canonical username/password.
Restrict `22323` and `29888` to trusted LAN CIDRs. See
[`local-server.md`](local-server.md) before exposing the service to other
devices.

If `management.password` is empty, EasyProxy accepts only a loopback management
listener. A wildcard, LAN, or other non-loopback management address requires a
password; Local Server mode derives it from `local_server.auth`.

### 7. Validate

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

<a id="fork-operator-guide"></a>

> 边界：`scripts/deploy-misub.ps1` 的 Pages 分支和
> `scripts/deploy-ech-workers-cloudflare.ps1` 是仍保留的低层手动部署入口，
> 不是下述受保护升级编排，也不获取 SBA writer。前者直接构建并执行 Pages deploy，
> 后者先写 ECH_TOKEN 再 deploy，不能提供备份、精确现有资源身份校验、
> bootstrap/update 区分及 UNKNOWN 停写保证。维护现有实例应选择其所属的
> 根工作流/CLI或 SBA 生命周期，不要对同一实例混用这些写入路径。

## Fork operator guide

This is the public, source-code-free path for creating and operating an
independent EasyProxy topology. Replace every angle-bracket placeholder. Never
reuse the maintainer's Cloudflare resources, URLs, image namespace, or secrets.

### Fork and enable automation

1. In GitHub, fork this repository into the account or organization that will
   own the deployment.
2. In the fork, open **Actions** and enable workflows.
3. Under **Settings > Actions > General > Workflow permissions**, allow read
   and write access so release and GHCR workflows can publish into the fork.
4. Follow [Quick start](#quickstart) for recursive checkout and initialization.

The three upstream submodules are public and remain pinned by the root commit.
The fork operator does not need to create separate forks of those repositories.

### Create an isolated provider scope

Use a dedicated Cloudflare account for bootstrap, failure injection, restore
drills, and teardown. Do not run destructive acceptance tests in an account
that contains unrelated Pages, D1, Worker, R2, DNS, or route resources.

Choose and record unique names before configuring GitHub:

| Resource | Example test name |
| --- | --- |
| MiSub Pages project | `ep8-<owner>-misub` |
| MiSub D1 database | `ep8-<owner>-misub` |
| ECH Worker | repository default `proxyservice-ech-workers` in an empty account |
| Aggregator R2 bucket | `ep8-<owner>-aggregator` |
| Aggregator public host | `sub-test.example.net` |

`Deploy Cloudflare Apps` creates or reuses the exact MiSub Pages and D1 names.
The Aggregator R2 bucket is a prerequisite, not an output of that workflow:

1. Create the bucket in the dedicated account.
2. Create an R2 S3 API token restricted to that bucket.
3. Attach a public custom domain to the bucket.
4. Record the account ID, access-key ID, secret access key, bucket name, and
   public base URL. Do not store these values in tracked files.

### Configure GitHub variables and secrets

Open **Settings > Secrets and variables > Actions** in the fork. The complete
matrix is in [`security.md#github-secrets`](security.md#github-secrets). At minimum configure:

#### Repository variables

- `CLOUDFLARE_ACCOUNT_ID`
- `EASYPROXY_MISUB_PUBLIC_URL`
- `EASYPROXY_MISUB_CALLBACK_URL`
- `EASYPROXY_ECH_WORKER_PUBLIC_URL`
- `EASYPROXY_AGGREGATOR_PUBLIC_BASE_URL`
- `EASYPROXY_AGGREGATOR_EFFECTIVE_URL`, exactly
  `<public-base>/subs/effective.txt`
- optional `EASYPROXY_MISUB_D1_DATABASE_NAME`; otherwise the Pages project
  input is also used as the D1 name
- optional `EASYPROXY_MISUB_D1_DATABASE_BINDING`; when set it must be
  `MISUB_DB`

`CLOUDFLARE_ACCOUNT_ID` is deliberately a non-secret Repository variable, not
an Actions secret. When migrating an older repository, copy the exact existing
value into the variable and remove the obsolete secret only after a successful
preflight. Never substitute a different account ID during an update.

Leave `EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE` unset or `false` until the first
manual publication succeeds.

#### Repository secrets

- `CLOUDFLARE_API_TOKEN`
- `MISUB_ADMIN_PASSWORD`
- `MISUB_COOKIE_SECRET`
- `MISUB_MANIFEST_TOKEN`
- `MISUB_CRON_SECRET`
- `EASYPROXY_BACKUP_PASSPHRASE`
- `ECH_TOKEN`
- Aggregator credentials beginning with `EASYPROXY_AGGREGATOR_`, as listed in
  [`security.md#github-secrets`](security.md#github-secrets)

The workflows accept only `CLOUDFLARE_API_TOKEN`. Delete obsolete
`CLOUDFLARE_AUTH_EMAIL` and `CLOUDFLARE_GLOBAL_API_KEY` secrets after verifying
the least-privilege token; they are not fallback credentials.

The root `MISUB_*` secret names are deployment inputs. The workflow writes
their values to the MiSub Pages runtime names `ADMIN_PASSWORD`,
`COOKIE_SECRET`, and `MANIFEST_TOKEN`; operators do not create both sets.

Create the protected environments before recovery work:

- `easyproxy-misub-restore`, with required reviewers for every restore run;
- `easyproxy-ech-rotation`, with required reviewers for token rotation.

In `easyproxy-ech-rotation`, create Environment secrets `ECH_TOKEN_NEXT`,
`EASYPROXY_REPOSITORY_ADMIN_TOKEN`, and `EASYPROXY_MANAGEMENT_PASSWORD`.
Create repository variables `EASYPROXY_ROTATION_BASE_URL`,
`EASYPROXY_ROTATION_PROXY_URL`, and optionally `EASYPROXY_ROTATION_RUNNER`,
`EASYPROXY_MISUB_CONNECTOR_PROFILE_ID`, and
`EASYPROXY_MISUB_MANIFEST_PROFILE_ID`. Ordinary deployment must not set or
rotate `ECH_TOKEN_NEXT`.

### Bootstrap cloud applications

Run workflows from the fork's **Actions** tab.

#### MiSub and ECH

Run **Deploy Cloudflare Apps** with:

- `target`: `both`
- `deployment_mode`: `bootstrap`
- `misub_project_name`: the exact recorded test project
- `misub_branch`: `main`
- `run_verification`: `true`

This workflow is the deployment authority for MiSub. If the selected Pages
project was previously connected to GitHub or GitLab, the workflow disables
automatic production and preview deployments before publishing. This prevents
a later push to the MiSub fork from replacing the generated D1 binding with the
portable, unbound source template. Update the root submodule pointer, then run
**Deploy Cloudflare Apps** instead of relying on Cloudflare Git builds.

The run must identify one exact Pages project, one exact D1 database and one
exact Worker before it deploys. Run the same bootstrap a second time; it must
reuse those identities rather than create duplicates.

#### Aggregator

Run **Deploy Aggregator** with:

- `deployment_mode`: `bootstrap`
- `force_deploy`: `true`

Confirm that the public host serves all of these paths:

- `/manifests/stable.json`
- `/subs/effective.txt`
- `/internal/crawledsubs.json`

Only after the manual run and MiSub synchronization succeed should
`EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE` be set to `true`.

### Backup, update, and recovery

#### Normal update

1. Run **Backup MiSub**. Record its workflow run ID and artifact name
   `misub-backup-<run-id>`.
2. Run **Deploy Cloudflare Apps** with `deployment_mode=update` and
   `run_verification=true`. Update fails closed if exact resources or the
   encrypted pre-migration backup cannot be verified.
3. Run **Deploy Aggregator** with `deployment_mode=update`.
4. Verify that the stable manifest changed only after candidate validation and
   that last-known-good remains readable.

#### MiSub restore drill

The `easyproxy-misub-restore` Environment gates every restore workflow run, so
an authorized reviewer must approve this drill as well as production recovery.
This intentionally prevents an unreviewed job from receiving restore credentials.
Run **Restore MiSub** with:

- `mode`: `drill`
- the recorded `backup_run_id`
- the exact `backup_artifact`
- the protected Pages and D1 names

The workflow creates a run-scoped drill D1, restores and verifies it, then
deletes only that exact drill database. It does not modify production D1.

Production restore is a separate run with the same approval gate. Select
`mode=production`, enter
the exact protected D1 database ID in `confirm_production_database_id`, and pass
the `easyproxy-misub-restore` environment approval. The workflow takes and
retains an additional encrypted pre-restore backup.

#### ECH token rotation

Run **Rotate ECH Token** only after configuring a distinct `ECH_TOKEN_NEXT`, a
dedicated reachable EasyProxy validator, and the protected rotation
environment. Enter the exact Worker name. The workflow overlaps both tokens,
validates Worker/helper traffic and the EasyProxy candidate, switches MiSub,
persists the new repository secret, revokes the old token, and attempts an
automatic rollback after any failure. See [`cloud-operations.md#ech-lifecycle`](cloud-operations.md#ech-lifecycle).

#### Aggregator failure

Do not copy a failed candidate to stable. Verify that
`/manifests/stable.json` and its referenced immutable release remain unchanged;
recovery uses the previous stable manifest and last-known-good objects described
in [`cloud-operations.md#aggregator-publication`](cloud-operations.md#aggregator-publication).

### Publish and install local EasyProxy

Run **Publish GHCR Images** with a release tag owned by the fork. For a native
candidate, run **Publish GitHub Release** manually with `draft=true` and a tag
starting with `v` or `release-`.

Use [Quick start](#quickstart) for Docker deployment from the fork namespace
and [Native installation](#native-install-update) for checksum verification,
explicit fork repository selection, config preservation, and rollback commands.

Linux NAS installation, update, and rollback are documented in
[`../deploy/nas/README.md`](../deploy/nas/README.md). LAN proxy and management
API verification are documented in [`local-server.md`](local-server.md).

### Acceptance record and release promotion

Before making a major release non-draft, retain all of the following:

- fork URL and root commit;
- recursive submodule commits;
- Cloudflare account ID and exact resource names/IDs, but no credentials;
- Bootstrap, second-Bootstrap, Update, backup, restore-drill, rotation, and
  Aggregator workflow run IDs;
- Linux amd64, Linux arm64, Windows amd64, and NAS runtime results;
- config and SQLite sentinels before update, after update, and after rollback;
- GHCR digests, native checksums, release manifest, and attestations;
- explicit confirmation that Windows arm64 remains unsupported.

Teardown is allowed only in the dedicated test account after this inventory is
complete and an operator approves the exact resource list. Match the recorded
test prefix and provider IDs; never use wildcard deletion and never put teardown
inside Bootstrap or Update.

A major release may be promoted from draft only after every
required acceptance item has evidence. A green build alone is not sufficient.

<a id="native-install-update"></a>

## Native install, update, and rollback

### Support matrix

| Product | Platform | Status |
| --- | --- | --- |
| EasyProxy service | Linux amd64 | Supported release archive and systemd installer |
| EasyProxy service | Linux arm64 | Supported release archive and systemd installer |
| EasyProxy service | Windows amd64 | Supported release archive and native Windows Service installer |
| EasyProxy service | Windows arm64 | Unsupported; no artifact is published |
| `easyproxyctl` | same supported targets | Separate matching archive |

Native packages support the Local Server, proxy pool, Web console, and
management API. Windows native transparent gateway mode is not supported.
Linux transparent gateway deployment remains the advanced Debian/Docker path.

### Stable filesystem contract

- Linux programs: `/opt/easyproxy/releases/<version>` with `current` and
  `previous` symlinks.
- Linux writable config authority: `/var/lib/easyproxy/config/config.yaml`.
- Linux bootstrap/migration copy: `/etc/easyproxy/config.yaml`.
- Linux state: `/var/lib/easyproxy`.
- Windows programs: `%ProgramFiles%\EasyProxy\releases\<version>`.
- Windows config and state: `%ProgramData%\EasyProxy`.

An update never replaces an existing config unless `--replace-config` on Linux
or `-ReplaceConfig` on Windows is explicitly supplied. Both installers stop the
service before copying config and SQLite state into a timestamped backup.
On the first Linux update from an older installation, the installer copies the
existing `/etc/easyproxy/config.yaml` into the writable state path before taking
the backup; the original remains available as the bootstrap/rollback copy.

### Linux

```sh
owner_repo='<OWNER>/<REPOSITORY>'
version='<RELEASE_TAG>'
base="https://github.com/${owner_repo}/releases/download/${version}"
curl -fLO "${base}/install.sh"
curl -fLO "${base}/SHA256SUMS"
grep '  install.sh$' SHA256SUMS | sha256sum -c -
sudo sh install.sh --version "${version}" --repository "${owner_repo}"
# Alternative to the preceding install, not a subsequent command:
# Explicit config replacement only when installing a not-yet-installed version.
sudo sh install.sh --version "${version}" --repository "${owner_repo}" --replace-config
# Separate recovery operation: restore program pointer and data snapshot.
sudo sh install.sh --rollback /var/lib/easyproxy/backups/before-<RELEASE_TAG>-...
```

The installer downloads the platform archive plus `SHA256SUMS`, verifies the
hash, installs the systemd unit, enables startup, and requires the service to be
active. `--archive` supports an already downloaded verified package.

### Windows

Run an elevated PowerShell prompt:

```powershell
$repository = '<OWNER>/<REPOSITORY>'
$version = '<RELEASE_TAG>'
$base = "https://github.com/$repository/releases/download/$version"
Invoke-WebRequest "$base/install-service.ps1" -OutFile .\install-service.ps1
Invoke-WebRequest "$base/SHA256SUMS" -OutFile .\SHA256SUMS
$expected = (((Get-Content .\SHA256SUMS | Where-Object { $_ -match '\s+install-service\.ps1$' }) -split '\s+')[0]).ToLowerInvariant()
$actual = (Get-FileHash .\install-service.ps1 -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw 'installer checksum mismatch' }
# Normal installation/update; preserve existing configuration.
.\install-service.ps1 -Version $version -Repository $repository
```

The following are alternative operations, not subsequent steps. Both installers
reject a release version that is already installed. To deliberately replace the
configuration during installation of a new version, use only the first command
below; to restore a selected backup instead, use only the second.

```powershell
# Optional: replaces active configuration with the new package example.
.\install-service.ps1 -Version $version -Repository $repository -ReplaceConfig
# Alternative: restore program and data from a verified, selected backup.
.\install-service.ps1 -Rollback -BackupPath 'C:\ProgramData\EasyProxy\backups\before-<RELEASE_TAG>-...'
```

For downloaded packages, the installer verifies `SHA256SUMS`. With an explicit
`-PackagePath`, it does not perform that download/checksum step: independently
verify the local package against the trusted release checksums before invoking it.
The installer registers a real SCM service with an
absolute config path, enables automatic startup, opens TCP ports 22323/29888,
and waits for the service to enter `Running`. Keep a copy of the selected
backup until the updated service has passed application-level checks.

<a id="root-host-deploy-standard"></a>

## Root Host Deploy Script Standard

This repository must preserve a repository-root deployment entrypoint named:

- `deploy-host.ps1`

The contract for that file is:

1. It must be a single-file operator entrypoint.
2. It must remain usable even when an operator downloads only this one file
   from GitHub.
3. It must not require the operator to manually clone or checkout the full
   repository before the first run.
4. If the local repository layout is missing, the script must bootstrap a
   local repo cache automatically before invoking any canonical internal
   deploy scripts.
5. If the full repository is already present, the script should fast-path to
   the local checkout instead of downloading another copy.
6. README deployment examples must point operators at `deploy-host.ps1`
   first, not at a deeper helper script.
7. The single-file entrypoint may delegate to repository-local scripts only
   after it has ensured the required repo files are present locally.
8. When the product supports blank-host deployment from GHCR or import-code
   style bootstrap, the root script should prefer that path over local-image
   shortcuts.
9. The script exposes `-ResolveRepoOnly` to resolve the source checkout without
   starting deployment or the runtime. It may still create a source cache and
   download recursive Git submodules; it is not a filesystem/network-free check.

Future deploy changes must preserve this contract.

### Optional runtime snapshots

The entrypoint accepts `-ImportCode` or `-BootstrapFile` for an explicit private
runtime snapshot. This is not required for an ordinary release. See
[Configuration distribution](#service-base-config-distribution) for authority,
secret-handling, publication, and bootstrap rules.

### Canonical Docker Naming

This repo must keep the short slug `easy-proxy` as the operator-facing
default for:

- Docker Compose project name
- default container name
- default network alias
- default local image name `easy-proxy/easy-proxy:local`

<a id="service-base-config-distribution"></a>

## Runtime Config Snapshot Distribution

EasyProxy can publish an **explicit snapshot** of the current local runtime
config to private Cloudflare R2. This is an advanced owner operation, not the
normal topology deployment path and not an automatic configuration authority.

### Authority and safety

- `deploy/service/base/config.yaml` (or the explicitly selected runtime file)
  remains the local runtime authority.
- `topology.yaml` supplies the deterministic R2 bucket name and environment
  variable references.
- Publication uploads the selected runtime file as-is; it does not render it
  from another root config.
- An ordinary deploy never downloads or overwrites a present runtime file.
- Enabling background R2 synchronization deliberately grants the remote object
  later-write authority and must be an explicit operator decision.
- Runtime snapshots may contain proxy credentials and connector tokens. Keep
  the bucket private and never upload the snapshot as a public GitHub artifact.

Backup and restore workflows use the lifecycle backup contract, not runtime
snapshot publication.

### Object layout

The local publisher writes:

```text
runtime/<deployment_name>/config.yaml
runtime/<deployment_name>/manifest.json
```

The distribution manifest records the account, bucket, endpoint, object keys,
size, content type, release version, and config fingerprint. It is distinct from
`deployment-manifest.json`, which records topology/resource/source versions.

### Local publication

Prerequisites:

1. a validated `topology.yaml`;
2. an existing runtime config;
3. the account and R2 secret references resolved in the process environment;
4. Python with `boto3`.

```powershell
$env:CLOUDFLARE_ACCOUNT_ID = '<account-id>'
$env:R2_ACCESS_KEY_ID = '<writer-key-id>'
$env:R2_SECRET_ACCESS_KEY = '<writer-secret>'

powershell -ExecutionPolicy Bypass -File .\scripts\publish-service-base-config.ps1 `
  -TopologyPath .\topology.yaml `
  -RuntimeConfigPath .\deploy\service\base\config.yaml `
  -ReleaseVersion <release-version>
```

Credentials are transferred to the Python uploader through the child process
environment, not command-line arguments. The script never prints their values.

### Bootstrap/import compatibility

Existing source-less runtimes can still consume an explicit bootstrap JSON or
an owner-provided import code:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\write-service-base-r2-bootstrap.ps1 `
  -ImportCode '<easyproxy-import-v1...>' `
  -OutputPath .\deploy\service\base\bootstrap\r2-bootstrap.json
```

Then deploy with one explicit bootstrap input:

```powershell
.\scripts\deploy-easyproxy.ps1 `
  -TopologyPath .\topology.yaml `
  -BootstrapFile .\deploy\service\base\bootstrap\r2-bootstrap.json
```

Do not provide both `-BootstrapFile` and `-ImportCode`. Never commit bootstrap
JSON, import codes, decrypted bundles, private keys, or downloaded runtime
configs.

### Update rule

Code updates preserve runtime data by default. When an update creates a backup,
it must verify that backup before deployment. A missing snapshot never grants
permission to replace the current config or create a second state resource.

当前 `write-service-base-r2-bootstrap.ps1` 写出的 bootstrap 默认
`syncEnabled: true`、`syncIntervalSeconds: 7200`，即允许后续后台同步；
该脚本不会继承 import code 中的同步开关。若只需要一次导入，必须在使用前
将生成的私有 JSON 明确改成 `syncEnabled: false`。不含该字段的旧 bootstrap
也被当前消费者按 `true` 处理。此处描述现有兼容语义，不代表默认已禁止远端更新。

### Import-code key and recovery tools

Use the shared Python CLI rather than the retired PowerShell key-generation and
decryption wrappers. Install `requirements-ci.txt` (including PyNaCl), run from
the repository root, and keep all outputs in an access-controlled private directory.
Python relative paths resolve against the current working directory; absolute
paths avoid dependence on where the shell was opened.

```powershell
python scripts/easyproxy-import-code.py generate-keypair --public-key-output '<private-dir>/owner-public.txt' --private-key-output '<private-dir>/owner-private.txt'
python scripts/easyproxy-import-code.py decrypt --encrypted-file '<private-dir>/encrypted.json' --private-key-file '<private-dir>/owner-private.txt' --import-code-only --output '<private-dir>/import-code.txt'
```

Always supply output paths: omitting them can print private keys or decrypted
credentials to the terminal. Do not commit these outputs.
