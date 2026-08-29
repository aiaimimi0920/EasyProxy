# Fork operator guide

This is the public, source-code-free path for creating and operating an
independent EasyProxy topology. Replace every angle-bracket placeholder. Never
reuse the maintainer's Cloudflare resources, URLs, image namespace, or secrets.

## 1. Fork and enable automation

1. In GitHub, fork this repository into the account or organization that will
   own the deployment.
2. In the fork, open **Actions** and enable workflows.
3. Under **Settings > Actions > General > Workflow permissions**, allow read
   and write access so release and GHCR workflows can publish into the fork.
4. Clone the fork recursively:

   ```powershell
   git clone --recurse-submodules https://github.com/<OWNER>/<REPOSITORY>.git
   Set-Location <REPOSITORY>
   git submodule status --recursive
   ```

The three upstream submodules are public and remain pinned by the root commit.
The fork operator does not need to create separate forks of those repositories.

## 2. Create an isolated provider scope

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

## 3. Configure GitHub variables and secrets

Open **Settings > Secrets and variables > Actions** in the fork. The complete
matrix is in [`github-secrets.md`](github-secrets.md). At minimum configure:

### Repository variables

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

Leave `EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE` unset or `false` until the first
manual publication succeeds.

### Repository secrets

- `CLOUDFLARE_API_TOKEN`
- `MISUB_ADMIN_PASSWORD`
- `MISUB_COOKIE_SECRET`
- `MISUB_MANIFEST_TOKEN`
- `MISUB_CRON_SECRET`
- `EASYPROXY_BACKUP_PASSPHRASE`
- `ECH_TOKEN`
- Aggregator credentials beginning with `EASYPROXY_AGGREGATOR_`, as listed in
  [`github-secrets.md`](github-secrets.md)

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

## 4. Initialize tracked and local configuration

Create the two configuration authorities in the clone:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-topology.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\init-runtime-config.ps1
Set-Location tools/easyproxyctl
go run .\cmd\easyproxyctl topology validate --file ..\..\topology.yaml
go run .\cmd\easyproxyctl topology names --file ..\..\topology.yaml
Set-Location ..\..
```

`topology.yaml` holds resource choices and names, never secret values.
`deploy/service/base/config.yaml` is local runtime state and is not replaced by
ordinary cloud or application updates.

## 5. Bootstrap cloud applications

Run workflows from the fork's **Actions** tab.

### MiSub and ECH

Run **Deploy Cloudflare Apps** with:

- `target`: `both`
- `deployment_mode`: `bootstrap`
- `misub_project_name`: the exact recorded test project
- `misub_branch`: `main`
- `run_verification`: `true`

The run must identify one exact Pages project, one exact D1 database and one
exact Worker before it deploys. Run the same bootstrap a second time; it must
reuse those identities rather than create duplicates.

### Aggregator

Run **Deploy Aggregator** with:

- `deployment_mode`: `bootstrap`
- `force_deploy`: `true`

Confirm that the public host serves all of these paths:

- `/manifests/stable.json`
- `/subs/effective.txt`
- `/internal/crawledsubs.json`

Only after the manual run and MiSub synchronization succeed should
`EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE` be set to `true`.

## 6. Backup, update, and recovery

### Normal update

1. Run **Backup MiSub**. Record its workflow run ID and artifact name
   `misub-backup-<run-id>`.
2. Run **Deploy Cloudflare Apps** with `deployment_mode=update` and
   `run_verification=true`. Update fails closed if exact resources or the
   encrypted pre-migration backup cannot be verified.
3. Run **Deploy Aggregator** with `deployment_mode=update`.
4. Verify that the stable manifest changed only after candidate validation and
   that last-known-good remains readable.

### MiSub restore drill

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

### ECH token rotation

Run **Rotate ECH Token** only after configuring a distinct `ECH_TOKEN_NEXT`, a
dedicated reachable EasyProxy validator, and the protected rotation
environment. Enter the exact Worker name. The workflow overlaps both tokens,
validates Worker/helper traffic and the EasyProxy candidate, switches MiSub,
persists the new repository secret, revokes the old token, and attempts an
automatic rollback after any failure. See [`ech-lifecycle.md`](ech-lifecycle.md).

### Aggregator failure

Do not copy a failed candidate to stable. Verify that
`/manifests/stable.json` and its referenced immutable release remain unchanged;
recovery uses the previous stable manifest and last-known-good objects described
in [`aggregator-publication.md`](aggregator-publication.md).

## 7. Publish and install local EasyProxy

Run **Publish GHCR Images** with a release tag owned by the fork. For a native
candidate, run **Publish GitHub Release** manually with `draft=true` and a tag
starting with `v` or `release-`.

Docker deployment from the fork namespace:

```powershell
.\scripts\deploy-subproject.ps1 `
  -Project easyproxy-ghcr `
  -TopologyPath .\topology.yaml `
  -GhcrOwner <OWNER> `
  -ReleaseTag <RELEASE_TAG>
```

Native installers require the fork repository explicitly:

```sh
sudo sh install.sh --version <RELEASE_TAG> --repository <OWNER>/<REPOSITORY>
```

```powershell
.\install-service.ps1 `
  -Version <RELEASE_TAG> `
  -Repository <OWNER>/<REPOSITORY>
```

Linux NAS installation, update, and rollback are documented in
[`../deploy/nas/README.md`](../deploy/nas/README.md). LAN proxy and management
API verification are documented in [`local-server.md`](local-server.md).

## 8. Acceptance record and release promotion

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

The first optimized major release may be promoted from draft only after every
required acceptance item has evidence. A green build alone is not sufficient.
