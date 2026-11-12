# EasyProxy 云端运行、数据保护与恢复

## 适用范围

本文按生命周期、数据保护、ECH、节点发布与额度保护组织云端操作合同。

## 选择正确的执行路径

- **根仓库独立 Actions / CLI 路径**：下述 Cloudflare lifecycle、MiSub backup/restore、ECH rotation 和 Aggregator Safe Publication 描述对应根工作流；不把它们当作 SBA 的自动恢复授权。
- **SBA 套件路径**：云端可用池通过 SBA update/refresh 生成，遵守持久 writer 与 UNKNOWN 停写。入口和阶段合同见 [SBA 生命周期](../.sba/lifecycle.md) 与 [协调边界](../.sba/integration-notes.md)。
- 两条路径的写入协调和失败处理不同，不应对同一实例并行启用写入者，也不能把独立发布脚本的 rollback/retry 套用到 SBA UNKNOWN。
- 文档中的命令是操作说明，不替代权限、具体资源核验和所需确认。源码、静态测试及历史回执不证明当前线上健康。

## 目录

- [Cloudflare 生命周期](#cloud-lifecycle)
- [MiSub 数据保护与恢复](#misub-data-protection)
- [ECH 生命周期与 Token 轮换](#ech-lifecycle)
- [独立 Aggregator 安全发布](#aggregator-publication)
- [SBA 云端可用池](#cloud-usable-pool)
- [MiSub 自动发现来源](#misub-discovery-lifecycle)
- [订阅月额度保护](#subscription-traffic-quota)
- [准确 WRITER_UNKNOWN 恢复](#writer-idle-update-repair)

<a id="cloud-lifecycle"></a>

## EasyProxy Cloudflare lifecycle

This document defines the operator contract for the root-owned Cloudflare lifecycle. The forked applications own their runtime code; `easyproxyctl`, root workflows, topology, manifests, and data protection are controlled by this repository.

### Authority and identity

1. `topology.yaml` is the desired-state authority. Secrets in topology are environment-variable names, never values.
2. `easyproxyctl cloud bootstrap` may create a missing Pages project or D1 database. It re-lists the resource after creation and requires exactly one exact-name result.
3. `easyproxyctl cloud update` only resolves existing resources. A missing or ambiguous result fails; update never creates a replacement.
4. `easyproxyctl manifest build` records topology checksum, source commits, and provider resource IDs.
5. `easyproxyctl cloud verify` verifies the sealed manifest and requires exactly one Wrangler D1 binding whose binding, database name, and database ID match the resolved state.

The MiSub D1 binding is always `MISUB_DB`. Names alone are not sufficient identity for a destructive or data-bearing operation.

### Bootstrap

Use bootstrap only for a new topology:

```bash
easyproxyctl cloud bootstrap \
  --topology topology.yaml \
  --state-output cloud-resources.json \
  --wrangler-dir upstreams/misub
```

After resolution:

1. Materialize Wrangler configuration with the exact Pages name, D1 name, D1 ID, and `MISUB_DB` binding.
2. Build a deployment manifest with `--resource-id pages=...` and `--resource-id d1=...`.
3. Run `cloud verify` against the materialized config.
4. Apply D1 migrations.
5. Deploy Pages and run semantic API verification.

Bootstrap is reentrant: a second bootstrap reuses the same exact resources and does not create duplicates.

Fork operators normally run this through **Actions > Deploy Cloudflare Apps**
with `deployment_mode=bootstrap`, `target=both`, an exact Pages project name,
and verification enabled. The complete prerequisite and input sequence is in
[`deployment.md#fork-operator-guide`](deployment.md#fork-operator-guide).

### Update

The protected order is mandatory:

1. Resolve resources in update mode; missing resources fail closed.
2. Materialize and verify the exact runtime config and deployment manifest.
3. Create an encrypted backup and retain it outside the deployment workspace.
4. Apply expand-only D1 migrations.
5. Deploy application code.
6. Run authenticated runtime verification.

`.github/workflows/deploy-cloudflare.yml` implements this order. A failed backup or artifact upload prevents migration and deployment. The workflow does not treat arbitrary resource-creation errors as “already exists.”

### Verify

```bash
easyproxyctl cloud verify \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --state-output cloud-resources.json \
  --wrangler-dir upstreams/misub
```

Verification is local/control-plane attestation. The deploy workflow additionally checks public HTML, public config, authenticated login/settings, a non-empty manifest profile, and Cron status.

Runtime verifier secrets are read from environment variables. Never pass passwords, manifest tokens, access tokens, cookies, or backup passphrases as command-line arguments.

Worker update also performs an exact script-name lookup before deployment.
Token rotation is not part of update; its overlap, connector switch, real
traffic validation, revocation, and rollback contract is documented in
[`cloud-operations.md#ech-lifecycle`](#ech-lifecycle).

### Backup and restore

完整加密备份格式、drill 和 production restore 的命令与失败处理统一见
[MiSub 数据保护与恢复](#misub-data-protection)。更新前备份的要求不变。

### Migrations

MiSub migrations live in `upstreams/misub/migrations` and are applied with:

```bash
npx wrangler d1 migrations apply <exact-database-name> --remote
```

Rules:

- migrations are expand-only during mixed-version operation;
- old code must continue to run against the expanded schema;
- no table or column removal is allowed in the same release as its replacement;
- destructive contract cleanup is a later, separately approved stable release;
- a populated update must have a retained encrypted backup before migration begins.

### GitHub configuration

Required repository secrets for MiSub lifecycle:

- `CLOUDFLARE_API_TOKEN`
- `MISUB_ADMIN_PASSWORD`
- `MISUB_COOKIE_SECRET`
- `MISUB_MANIFEST_TOKEN`
- `EASYPROXY_BACKUP_PASSPHRASE`

Required repository variables:

- `CLOUDFLARE_ACCOUNT_ID`
- `EASYPROXY_MISUB_PUBLIC_URL`
- `EASYPROXY_MISUB_CALLBACK_URL`
- optionally `EASYPROXY_MISUB_D1_DATABASE_NAME`
- optionally `EASYPROXY_MISUB_D1_DATABASE_BINDING` (must resolve to `MISUB_DB`)

Use a high-entropy backup passphrase independent of the MiSub admin password.
Configure approval rules for the `easyproxy-misub-restore` GitHub Environment
before running either a restore drill or production recovery.

---

<a id="misub-data-protection"></a>

## MiSub data protection and restore

### What is protected

Each `.age` backup is an encrypted `age` scrypt envelope containing a gzip/tar archive with exactly four entries:

- `database.sql`: full remote D1 export;
- `logical-backup.json`: complete logical sources, profiles, settings, Cron state, counts, and logical checksum;
- `deployment-manifest.json`: sealed topology/resource/source attestation;
- `backup-metadata.json`: source database name, ID, binding, schema version, row counts, row snapshot checksum, and per-file SHA-256 values.

The archive extractor rejects unknown names, duplicate entries, unsafe entry types, oversized entries, wrong passphrases, modified ciphertext, malformed metadata, and file checksum mismatches. Output files use private permissions and are never overwritten.

The preferred logical export is the authenticated MiSub API. For an upgrade from an older deployed MiSub that lacks this endpoint, `easyproxyctl` builds the same logical payload directly from the exact D1 storage rows. The D1 snapshot is taken before and after export; if it changes, the tool retries and refuses to publish an inconsistent backup after three attempts.

### Create a backup

Set the secret values named by topology:

```bash
export CLOUDFLARE_API_TOKEN='...'
export CLOUDFLARE_ACCOUNT_ID='...'
export MISUB_ADMIN_PASSWORD='...'
export EASYPROXY_BACKUP_PASSPHRASE='...'
```

Then run:

```bash
easyproxyctl cloud backup \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --wrangler-dir upstreams/misub \
  --base-url https://your-misub.example \
  --allow-direct-d1-fallback \
  --output backups/misub-$(date +%Y%m%d-%H%M%S).age
```

`--base-url` exercises authenticated full export and validates runtime resource identity. Direct-D1 fallback is disabled unless `--allow-direct-d1-fallback` is explicitly supplied; use it for protected upgrades from an older deployment that has no full-export endpoint. The resulting logical backup records `applicationVersion: d1-direct-export` so the fallback is auditable.

The manual GitHub equivalent is **Backup MiSub** (`backup-misub.yml`). It uploads only the encrypted archive. The passphrase is not stored in the artifact. The workflow artifact is named `misub-backup-<run-id>` and retained for 30 days.

Run **Restore MiSub** with the exact workflow run ID and artifact name. Both drill and production runs use the protected `easyproxy-misub-restore` Environment and require its approval before receiving credentials. Production additionally requires `confirm_production_database_id` matching the protected D1 and a retained pre-restore backup.

### Restore drill

Always run an isolated drill before considering production restore. Create a temporary D1 named with the protected database prefix:

```text
<production-database>-restore-drill-<unique-run-id>
```

Then run:

```bash
easyproxyctl cloud restore \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --wrangler-dir upstreams/misub \
  --input backups/misub.age \
  --target-database-name <drill-name> \
  --target-database-id <drill-id> \
  --confirm-database-id <drill-id> \
  --drill
```

The tool requires exactly one target matching the supplied name and ID, rejects the production D1 as a drill target, imports the SQL, and compares schema version, per-table row counts, and canonical database-row SHA-256 with backup metadata.

The **Restore MiSub** workflow defaults to `drill`. It creates a uniquely named D1, runs the restore verification, and deletes only that exact run-created D1 after rechecking its name and ID.

### Production restore

Production restore is intentionally harder:

- run it only through a protected operator session or the approval-gated `easyproxy-misub-restore` GitHub environment;
- provide the exact production D1 ID as confirmation;
- pass `--allow-production-restore`;
- provide a new, non-existing `--pre-restore-backup` output path;
- provide the current MiSub base URL.

Example:

```bash
easyproxyctl cloud restore \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --wrangler-dir upstreams/misub \
  --input backups/known-good.age \
  --target-database-name <production-name> \
  --target-database-id <production-id> \
  --confirm-database-id <production-id> \
  --allow-production-restore \
  --allow-direct-d1-fallback \
  --base-url https://your-misub.example \
  --pre-restore-backup backups/immediately-before-restore.age
```

The pre-restore backup must finish before SQL import starts. Retain both the original recovery archive and the automatic pre-restore archive. A restore command never creates or deletes a D1 database; only the drill workflow manages the temporary D1 it created itself.

### Failure rules

- Missing, ambiguous, or mismatched Cloudflare resources: stop.
- Manifest, topology, D1 binding, source identity, archive, or checksum mismatch: stop.
- Backup artifact upload failure: do not migrate or deploy.
- Database changes during backup: retry, then stop without publishing.
- Restore verification mismatch: treat the target as invalid; do not bind or deploy it.
- Lost passphrase: the encrypted backup is unrecoverable. Store it in an independent password manager.

---

<a id="ech-lifecycle"></a>

## ECH Worker lifecycle

This document is the operator contract for the first-party Cloudflare Worker,
the forked Go helper, MiSub connector state, and EasyProxy validation.

### Stable identity and ordinary updates

`workers/ech-workers-cloudflare/wrangler.jsonc` owns the Worker name. Before an
update, `scripts/verify-cloudflare-worker-identity.py` performs an exact
Cloudflare API lookup. Update fails when that name is absent; it never deploys a
replacement Worker under a guessed name. Bootstrap may create the configured
name.

An ordinary `.github/workflows/deploy-cloudflare.yml` run:

1. requires the existing `ECH_TOKEN` secret;
2. does not generate or rotate a token;
3. deploys to the tracked Worker name and existing public URL;
4. verifies the exact Cloudflare identity, HTTP banner, current token, invalid
   token rejection, and a remote TCP request through the Worker;
5. synchronizes MiSub while preserving managed `server_ip` values by default.

Use `--drop-server-ips` with `scripts/sync-misub-ech-profile.py` only when the
operator intentionally wants to discard those values.

### Safe token rotation

Token rotation is separate from deployment. Run **Rotate ECH Token** only after
configuring the protected `easyproxy-ech-rotation` GitHub Environment.

Required rotation secrets:

| Name | Purpose |
| --- | --- |
| `ECH_TOKEN` | Current canonical token. |
| `ECH_TOKEN_NEXT` | New token; must differ from the current token. |
| `EASYPROXY_REPOSITORY_ADMIN_TOKEN` | Fine-grained token used only to replace the repository `ECH_TOKEN` secret; requires repository Secrets write permission. |
| `EASYPROXY_MANAGEMENT_PASSWORD` | Authenticates the dedicated EasyProxy validation instance. |
| `MISUB_ADMIN_PASSWORD` | Updates and rolls back MiSub connector state. |
| `MISUB_MANIFEST_TOKEN` | Verifies candidate and final MiSub manifests. |
| Cloudflare credentials | Updates Worker code and secrets for the exact account/name. |

Required variables:

| Name | Purpose |
| --- | --- |
| `EASYPROXY_ECH_WORKER_PUBLIC_URL` | Existing public Worker URL. |
| `EASYPROXY_MISUB_PUBLIC_URL` | MiSub control-plane URL. |
| `EASYPROXY_ROTATION_BASE_URL` | Management URL for a dedicated EasyProxy validator reachable from the selected runner. |
| `EASYPROXY_ROTATION_PROXY_URL` | HTTP proxy URL for the same validation instance. |
| `EASYPROXY_ROTATION_RUNNER` | Optional JSON runner label array; defaults to `["ubuntu-latest"]`. |

The validation EasyProxy instance must use the target MiSub profile and must be
isolated so that its validation proxy traffic cannot silently fall back to an
unrelated connector. A self-hosted runner is appropriate when this instance is
reachable only inside a LAN.

The workflow performs these guarded stages:

1. confirm the exact Worker name and save the old token only in a private runner
   temporary file;
2. publish dual-token-capable code while the old token is still current;
3. set the new current token and a time-limited previous-token binding;
4. prove old and new tokens through Worker TCP tunnels and through the Go helper
   using both HTTP and SOCKS5;
5. add a MiSub candidate connector without deleting the old connector;
6. wait for the dedicated EasyProxy instance to load the candidate and complete
   a real proxy request;
7. switch MiSub to the canonical connector and persist `ECH_TOKEN_NEXT` as the
   repository `ECH_TOKEN` secret;
8. expire, verify rejection of, and remove the old Worker token.

Any failure after mutation triggers a best-effort rollback of Worker current
token, MiSub connector state, and the repository canonical secret. A failed
rollback keeps the workflow failed and requires operator intervention; it is
never reported as success.

### Worker token semantics

The Worker always requires `ECH_TOKEN`. During rotation it also accepts
`ECH_TOKEN_PREVIOUS` only when `ECH_TOKEN_PREVIOUS_EXPIRES_AT` parses as a
strictly future ISO timestamp or Unix epoch. Missing, malformed, or expired
previous-token metadata is fail-closed. Arbitrary invalid tokens remain rejected.

The overlap maximum is 240 minutes. The workflow first expires the previous
token, proves it is rejected, and then deletes both temporary bindings.

### Go helper release

`upstreams/ech-workers` is the public fork submodule. Its tag workflow uses the
Go version declared in `go.mod`, runs all packages before cross-compilation,
publishes Linux/macOS/Windows binaries, and includes `SHA256SUMS`.

The helper reads authentication from `ECH_TOKEN` when `-token` is omitted. Use
the environment variable in automation so the token is absent from process
arguments. `scripts/verify-ech-helper-e2e.py` starts the helper and proves both
HTTP and SOCKS5 traffic through a real Worker tunnel.

### Live acceptance evidence

Local unit tests and workflow validation do not prove a production rotation.
Phase acceptance requires one protected workflow run against real Cloudflare,
MiSub, and a dedicated EasyProxy validation instance. Retain the workflow URL,
Worker name, root and submodule commits, overlap/revocation results, and rollback
result without retaining secret values.

### Socket shutdown and production diagnostics

TCP sockets expose independent `opened` and `closed` promises. Handle `closed`
rejections even when connection setup fails, and observe the promise returned
by `close()`. Client disconnects during `opened` must not reacquire streams or
send `CONNECTED`. `src/session.test.mjs` covers these failure and cleanup paths.

The helper verifier uses `connectivitycheck.gstatic.com/generate_204` and checks
the HTTP status, not merely the presence of an HTTP header. Cloudflare TCP
socket restrictions can reject Cloudflare-hosted destinations, including a
probe hostname that changes hosting. Such a rejection does not mean that every
Worker tunnel is broken; use both a supported target and a failing-target probe.

A separate Cloudflare runtime issue can record `Network connection lost.` when
an otherwise valid WebSocket is closed by the client without application data:
https://github.com/cloudflare/workerd/issues/5290 . Correlate the exact request
and Worker version in tail logs. Do not disable observability or count an HTTP
502 generated by the local helper as a successful end-to-end proxy request.

---

<a id="aggregator-publication"></a>

## Aggregator Safe Publication

For the standalone Actions publication path, the root `Deploy Aggregator` workflow is the publication authority.
The Aggregator fork computes artifacts; first-party scripts isolate, validate,
and promote them in R2. Workflows inside the submodule are references only and
must not be enabled as a second writer.

The workflow also uses a non-cancelling GitHub Actions concurrency group. This
is the supported single-writer boundary. Direct R2 writes and a second enabled
publication workflow are unsupported because they bypass rollback snapshots and
the stable-manifest commit order.

### Object layout

For release ID `<run-id>` and a canonical key such as `subs/effective.txt`:

| Layer | Key | Contract |
| --- | --- | --- |
| Run candidate | `candidate/<run-id>/subs/effective.txt` | Native process and first-party publishers write only here. |
| Candidate alias | `candidate/subs/effective.txt` | Latest fully validated candidate. |
| Immutable release | `releases/<run-id>/subs/effective.txt` | Content-addressed by the release manifest hash; never reused. |
| Stable | `subs/effective.txt` | Compatibility path for MiSub and EasyProxy. |
| Stable commit pointer | `manifests/stable.json` | Written last after every stable object passes public verification. |
| Last known good | `last-known-good/subs/effective.txt` | Snapshot of the previous stable object. |

R2 updates one object atomically, but it does not provide a transaction across
multiple object keys. Therefore `manifests/stable.json` is the release commit
point. Fixed stable paths are updated and publicly verified before that
manifest changes; any error restores every attempted key from the pre-promotion
snapshot. Consumers that need a consistent multi-file view must read the stable
manifest and use its immutable `release_key` entries.

### Publication gates

The workflow performs these steps in order:

1. materialize source credentials without committing them;
2. rewrite every Aggregator storage key into `candidate/<run-id>/...`;
3. run the upstream crawler and converters;
4. generate the V2Ray-compatible candidate from this run's mixed subscription;
5. validate all configured generated artifacts, including V2Ray, before
   the expensive connectivity audit (`--before-effective-audit`); only the two
   not-yet-generated effective outputs are deferred;
6. run the shared EasyProxy connectivity audit against candidate Clash output;
7. produce `public-effective` and `public-effective-json` from audited nodes;
8. require all configured public candidate artifacts;
9. enforce absolute node/source minimums and relative decline limits;
10. publish and verify immutable release objects;
11. snapshot the previous stable objects, update stable, and write the stable
    manifest last.

Failures before step 11 never touch stable. Failures during step 11 restore the
previous stable bytes and leave the previous manifest as the committed version.
Candidate and immutable release objects may remain for diagnosis.

#### V2Ray 兼容产物

仓库固定版本的 subconverter 在 `target=v2ray` 下仅导出 VMess；当候选只有
SS、VLESS 或 Trojan 时会成功退出却生成空文件。`list` 开关不改变这个协议范围。
根发布流程现在由 `scripts/publish-aggregator-v2ray.py` 从**本次 run** 的 mixed
候选生成兼容订阅，保留 `vmess/vless/trojan/ss/ssr`，与既有 verifier 合同一致。
模板不再同时让 native `v2ray` target 写同一对象；独立上游仓库不需修改。

生成器要求源和目标处于同一 bucket、同一隔离 candidate run，且 key 不同；拒绝
stable、release、跨 run、空内容、超限内容及非法 Base64/URI 行。如果没有兼容
协议，仍然失败，不以旧 stable 填充，也不放宽 `--require-all`。

此产物是 mixed 的协议兼容子集，**不是逐条连通性验证过的 effective 子集**。
节点网络可用性仍由后续共享审计和 effective 产物单独表达。

After canonical stable verification, the publication workflow refreshes the
MiSub runtime profile using the stable effective URL and the configured extra
subscriptions. Extra providers are audited for MiSub but are not copied into
the public R2 release. Existing connector references are retained. A failed
MiSub refresh fails the workflow without undoing the already verified R2 release.
The scheduled publication, Cloudflare deployment profile-sync job, and manual
ZenProxy sync share the publication concurrency group to avoid competing writes.

`MISUB_CRON_SECRET` is also persisted into MiSub settings by runtime sync. The
workflow invokes authenticated Cron after the profile refresh; built-in
`aggregatorSync.runOnCron` is disabled so Cron updates subscription metadata
without independently rewriting the runtime profile. Saving a GitHub secret
alone does not configure MiSub's database-backed Cron endpoint.

If a runner is terminated before its exception handler can restore objects, the
next publication reconciles every fixed stable key from the immutable release
named by the still-committed stable manifest before evaluating the new
candidate. Each fixed object write is atomic; readers of several related files
must still use immutable release keys from the manifest to avoid observing an
in-progress compatibility-key sequence.

### Fork configuration

Configure the secrets and variables in
[`security.md#github-secrets`](security.md#github-secrets), then enable the `Deploy Aggregator`
schedule with `EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE=true`. The default cron is
six-hourly. A fork may change the cron in the workflow without changing the
Aggregator source fork.

Recommended publication variables:

| Variable | Default | Meaning |
| --- | ---: | --- |
| `EASYPROXY_AGGREGATOR_MIN_STABLE_NODES` | `1` | Absolute effective-node floor. |
| `EASYPROXY_AGGREGATOR_MIN_SOURCE_COUNT` | `1` | Absolute crawled-source floor. |
| `EASYPROXY_AGGREGATOR_MAX_NODE_DROP_RATIO` | `0.60` | Reject a drop greater than 60% from prior stable. |
| `EASYPROXY_AGGREGATOR_MAX_SOURCE_DROP_RATIO` | `0.80` | Reject a drop greater than 80% from prior stable. |

`EASYPROXY_AGGREGATOR_EFFECTIVE_URL` must be exactly
`<EASYPROXY_AGGREGATOR_PUBLIC_BASE_URL>/subs/effective.txt`. Cloudflare/MiSub
deployment verifies that URL against `manifests/stable.json` and the immutable
release copy before synchronizing MiSub. Local EasyProxy uses the same stable
URL only under `source_sync.fallback_subscriptions`, so the fallback remains
available when MiSub is unreachable.

### Recovery

Normal retry creates a new release ID and never reuses an immutable key. To
inspect or manually recover the previous version, use:

- `last-known-good/manifest.json` for the previous committed manifest;
- `last-known-good/<stable-key>` for its compatibility snapshot;
- `last-known-good/releases/<previous-run-id>/<stable-key>` for the immutable
  retained snapshot.

Do not point consumers at `candidate/`. It is an operator inspection layer, not
an availability contract.

---

<a id="cloud-usable-pool"></a>

## 云端可用池与本地 connector

### 目标与边界

普通订阅和 agg 采集节点在云端汇总并接受同一轮 EasyProxy 运行时审计。
NAS 使用的 `aggregator-global` manifest 只包含审计通过的实际代理 URI，以及选中的
ZenProxy/ECH connector 参数；不包含普通订阅的原始地址。

来源记录仍在 MiSub 独立管理。`kind=subscription` 且 `enabled=true` 的来源每轮都会读取，
与是否被某个普通订阅组勾选、上次格式探测是否成功无关。禁用或删除来源后，下一轮成功
刷新不再带入该来源的旧节点。现有 HTTPS 来源使用其 `customUserAgent`（默认 `clash.meta`）。
云端输入只接受无 URL 用户名密码、无 fragment 的公开 HTTPS 地址，重定向逐跳校验且最多三跳。

### 一条生成与发布链路

1. 正常 update 和周期 refresh 从准确项目 `pages.dev` 的备份导出接口读取来源登记；不调用
   有迁移/Cron 副作用的 `/api/data`，不把管理密码交给第三方订阅或 upstream 子进程。
2. 每个普通订阅重新下载到私有生成目录，再交给批准的 agg 解析/转换器。每个来源有单独的
   本地转换产物，检查它确实贡献了候选节点，不能以 HTTP 200 或整批 agg 成功掩盖漏源。
3. 所有普通订阅候选与 agg 候选合并、去重，交给准确版本 EasyProxy 镜像进行真实消费审计。
   下载成功、格式正确和转换成功均不代表节点可用。
4. 只有审计返回的 `stable_available_uris` 才进入 `subs/effective.*` 和 MiSub Global 托管节点。
   Global 的 `subscriptions` 固定为空；普通订阅 URL 不下发 NAS。
5. ZenProxy/ECH 不在云端预先展开为静态节点。Global 下发它们的 connector 参数，NAS 自行
   取节点或建立连接，并与所有普通代理节点一起接受本地健康检查和故障隔离。

原始候选格式产物用于生成/审计和兼容既有产物合同，不是 NAS 的可用池入口。客户端应使用
Global runtime manifest 或 `subs/effective.txt` / `subs/effective.json`，不能把 raw Clash
候选列表当作可用性承诺。云端可用与 NAS 所在网络可用仍是两个独立结论。

### 轮换与失败语义

每轮 Clash 候选附带不含订阅 URL、密钥或源内容的用户节点摘要。下一轮继承 agg 候选时，
先剔除这部分用户节点，再使用订阅的本轮内容；因此每日轮换、来源禁用/删除不会把旧订阅
节点经 `remains` 缓存继续带回。摘要不是健康结论，任何继承的 agg 节点也必须重新审计。

下载失败、空响应、某个来源没有可解析候选或转换后未进入合并池时，本轮生成明确失败，
不以空源伪装成功、不发布半批新池。生成在获取发布 writer 之前完成，因此这类失败保留
上一成功发布的池，不占用新的持久 writer。已进入发布阶段的 UNKNOWN 继续遵循原有停写规则。

### 周期

现有 GitHub Actions `sba-aggregator-refresh.yml` 每六小时运行一次（UTC `25 */6 * * *`；
北京时间 02:25、08:25、14:25、20:25，实际开始时间可能受 Actions 调度延迟影响）。
每次都从当前 active 的批准源码执行，重新读取 MiSub 登记、下载普通订阅、合并并重新审计。

NAS 现有 source-sync 每小时刷新一次可用池和 connector；不需要在 NAS 再保存两条原始订阅。
云端或本地刷新失败均不得被报告为本轮刷新成功。

### 历史部署证据

[2026-11-08 上线与验收记录](deployment-records.md#cloud-usable-pool-2026-11-08) 保存当次部署和业务验收。
它不是当前版本或节点数量的长期承诺；核验当前状态需重新读取实际发布版本、刷新回执及 NAS。

---

<a id="misub-discovery-lifecycle"></a>

## MiSub 自动发现来源的保留与清理

仅处理 `kind=subscription` 且 `options.managed_by=aggregator_sync` 的来源。手工机场、Aggregator Stable、健康检查来源、运行时代理和 Connector 不在删除范围。

### 判断规则

- 上游 export 缺失：MiSub 只标记 `aggregator_missing`，不再直接禁用。
- 已停用或上游缺失的发现来源：由 `Maintain MiSub Discovery` 每 6 小时复查。
- HTTP 200 不是节点可用的证明。非 HTML 响应需通过独立 Docker EasyProxy 审核，至少一个节点完成真实代理探测才判定健康。
- 上游缺失但审核健康：保留；若此前由缺失规则停用，则重新启用。
- 连续两次确认无效的链接（404/410、非挑战型 HTML 页面），或连续两次完成节点加载但实际代理探测失败，才删除来源，并清理订阅组引用。
- DNS/TLS、403、429、5xx、验证码、审核容器构建/启动异常等均视为不确定，保留供下一轮复查。节点审核失败时还需检查宿主网络连通性，避免断网导致批量误删。
- 写入前重新读取生产数据，有并发编辑则中止。采用增量补丁，不上传或记录订阅密钥。

脚本默认只预览，实际写入需 `--apply`：

```text
python scripts/maintain-misub-discovery.py --apply
```

认证从环境变量 `MISUB_ADMIN_PASSWORD` 读取；`MISUB_PUBLIC_URL` 指定站点。运行需要 Docker、requests、PyYAML，节点审核复用 `easyproxy_source_audit.py`。本地运行用 `--work-dir` 将审核产物指向 `linshi`；完整审核产物可能含节点凭据，禁止提交或作为公开 CI artifact 上传。

### ECH 订阅组

NAS 使用 `aggregator-global`，不依赖独立 `easyproxies-ech-runtime` 组。ECH 部署与 Token 轮换统一使用 `--connector-only --attach-profile-id aggregator-global`，只更新受管 ECH 来源引用，不重建独立组。删除该独立组不删除 Connector 或 Worker。

---

<a id="subscription-traffic-quota"></a>

## 月额度订阅保护

### 规则

MiSub 普通订阅登记的 `options.trafficQuota.enabled=true` 启用额度保护。
用户的 `enabled` 仍是手动总开关，额度检查不修改它。保护按机场响应头
`subscription-userinfo` 的 `(upload + download) / total` 判断；严格大于 99% 时
有效停用，恰好 99% 不停用。使用整数比较，不把套餐到期时间当成月度重置时间。
成功读取到重置或充值后低于等于阈值的额度，下一次检查自动允许使用。
未知、缺失、重复、非法或零总额度统计暂不允许受控来源，不视作零用量。

### 节点池与下发

每轮云端生成仍读取启用的受控来源，包括之前因额度被排除的来源；根据本轮响应头
决定是否进入转换与审计。未取得可用额度的来源不进入该轮候选；既有注册节点 hash
机制继续清除上一轮用户节点，避免 remains 缓存重新带回。

最终 Clash 候选中，受控来源贡献的节点名加上 `[epq:<来源ID的SHA-256>]` 标记，
不包含订阅地址、密钥或其可逆编码。审计后的 URI 保留该标记。多个受控来源共用
同一节点时，任一来源被阻止即阻止该节点，保守保护额度。

生产 MiSub 的认证 manifest 读取会实时查询受控来源额度，再过滤已发布的相应
托管节点。因此即使下一轮完整发布因 COUNT_DROP 等门禁失败，旧托管节点也不能
仅靠留在数据库中绕过本次成功 manifest 的额度检查。手动节点和 connector 不被
名称标记误过滤；关闭额度保护允许仍启用来源的旧标记节点，删除或手动停用来源
则拒绝其旧标记节点。初次启用保护需要成功生成带标记的候选；不能识别此前无标记的节点。

每次 manifest 的机场检查共用 10 秒预算、最大并发 4，超过预算的排队来源为 unknown。
检查不修改业务数据库、不覆盖手动开关，避免与备份及部署的数据保留校验竞争。
管理员可使用已登录 Cookie 读取 `/api/sba/quota-status` 查看状态、原始字节统计及检查时间。
preview 不访问真实机场、不执行生产额度过滤；认证和隔离规则保持。

### 生效边界

这不是实时硬限流：机场统计本身可能延迟，客户端在下一次成功获取 manifest 并
应用节点池后才停止选择该来源，已建立连接不保证即时终止。当前 NAS 检查时配置为
每小时同步、请求超时一分钟、无 fallback 或额外订阅。云端完整生成仍为每六小时；
已经从池中移除的节点在额度恢复后，需下一轮成功生成并下发才重新出现。

单机场额度获取失败会返回成功但排除该来源的 manifest；数据库故障、整个 manifest
请求失败或 NAS reload 失败仍遵循原有失败保留旧池语义，不能声称网络故障时也有硬限额。
保护针对云端生成和 MiSub runtime manifest 消费链；历史 R2 effective/raw 快照不被
原地改写，直接使用这些快照或手动复制节点的外部消费者不受动态下发过滤保护。

订阅 URL 仅保存在 MiSub 业务数据和本机私密备份中，不加入源码或测试夹具。

---

<a id="writer-idle-update-repair"></a>

## WRITER_UNKNOWN 升级恢复

`v1.0.23` 声明单一 SpringBok 修复动作 `continue-writer-idle-update`，用于恢复任务
`dc-11293c42b5134efabfefb45113f1691b` 的准确 `EASYPROXY_SBA_WRITER_UNKNOWN` 回执。
这是现有实例的应用修复，不是新建实例，也不是把失败任务改成成功。

### 为什么不能继续普通升级

SpringBok 仅对最近任务为 `succeeded` 或 `deployed-unverified` 的实例启用普通升级。
该父任务已消耗执行许可并返回 `unknown`，因此不符合未启动任务的“恢复升级”条件。
即使 EasyProxy 的写入占用已经解除，平台仍需通过正式 repair 新建关联执行记录。

### 修复边界

- 精确绑定父任务、run、请求摘要、结果摘要、执行器 SHA 和回执 artifact 摘要。
- 平台请求的 previous 是失败目标 `1.0.21`；实际云端 active 必须仍是 `1.0.20`。
- 在构建前和正式发布前执行只读检查：配置归属、准确父回执、writer 空闲、旧刷新恢复证据、
  稳定 manifest 及全部七项产物与公开字节一致。
- 任一检查失败即停止；不清锁、不接管新的 writer、不重放父任务、不跳过备份或迁移。
- 正式执行走正常 update：保留注册来源、读取实际 active 的生成输入，执行备份、数据保留、
  迁移、发布和业务验证；最终获得新的 repair 回执，旧 unknown 记录保留。
- 该动作只接受本次准确父回执及候选版本 `1.0.23`，不是通用恢复许可。

### 验收状态

请求边界、父回执读取、只读检查、正常 update 路由及相关部署回归已在本地验证。
生产升级是否成功，必须另行核对 SpringBok 新任务回执、实际发布版本及数据保留；
代码测试或发布 GitHub Release 不代表生产升级完成。
