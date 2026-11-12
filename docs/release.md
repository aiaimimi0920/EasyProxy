# EasyProxy 发布、验收与发布说明

## 适用范围

本文按主题集中维护相关合同，按目录查阅；不包含后续阶段计划。

## 目录

- [发布检查清单](#release-checklist)
- [发布产物合同](#release-contract)
- [发布说明模板](#release-notes-template)

<a id="release-checklist"></a>

## Release Checklist

Use this checklist before publishing a tag or manually running the GHCR release workflow.

### Config And Secrets

1. Validate `topology.example.yaml` and confirm it contains environment variable references only.
2. Confirm no live secrets were added to tracked files such as:
   - `deploy/service/base/config.yaml`
   - `deploy/service/base/bootstrap/r2-bootstrap.json`
   - `upstreams/misub/.env`
   - `workers/ech-workers-cloudflare/.dev.vars`
3. Confirm `.gitignore` still excludes local runtime data, generated config, and Python caches.
4. If Local Server is enabled, confirm:
   - `mode: pool`
   - `listener.protocol: mixed`
   - no `extra_listeners`
   - one non-placeholder `local_server.auth` username/password
   - host/router firewall rules block WAN and guest VLAN access to `22323/29888`

### Validation

Run or confirm the latest successful CI result for:

```powershell
Set-Location service/base
python ../../scripts/check-go-format.py cmd internal
go test -count=1 -timeout=300s ./...
go vet ./...

Set-Location frontend
npm ci
npm run test
npm run lint
npm run build

Set-Location ../../..
python -m pytest -q tests
python scripts/validate-release-contract.py
python scripts/build-native-release-manifest.py --directory <assembled-release-dir> --verify
git diff --check
```

If Local Server behavior, Profile APIs, embedded frontend assets, proxy
authentication, or deployment topology changed, build a fresh image and run:

```powershell
$tag = "easyproxy/easy-proxy-monorepo-service:local-server-release-check"
$validationId = "local-server-release-$(Get-Date -Format yyyyMMddHHmmss)"
docker build -f deploy/service/base/Dockerfile -t $tag .
powershell -NoProfile -ExecutionPolicy Bypass `
  -File deploy/service/base/scripts/validate-local-server-device-profiles.ps1 `
  -Image $tag -ValidationId $validationId -KeepArtifacts
```

Record the image ID and evidence directory. The validation must use disposable,
label-scoped resources and must not start, stop, replace, or attach the legacy
`easy-proxy` container.

CI workflows:

- `.github/workflows/validate.yml`
- `.github/workflows/publish-ghcr-images.yml`
- `.github/workflows/publish-github-release.yml`
- `.github/workflows/deploy-cloudflare.yml`
- `.github/workflows/deploy-aggregator.yml`

### What native publication does and does not validate

`publish-github-release.yml` builds all three target pairs and runs only
`--help` / CLI-version smoke checks on Linux amd64 and Windows amd64. Linux
arm64 is cross-built without that smoke execution. Its publish job depends on
metadata and native builds, not on completion of the separate repository
validation workflow. None of these steps replaces actual service startup,
proxy traffic, update, or rollback acceptance on the target systems.

The checklist above is an operator release gate, not an automatically enforced
end-to-end acceptance gate. A matching tag push publishes with `draft=false`;
a manual workflow defaults to `draft=true`. Complete and record the required
validation before pushing a public release tag or promoting a draft.

原生发布的三个 job 均检出本次 workflow 的准确 `github.sha`。
构建前与发布前会查询远端 tag：已有 tag（含 annotated tag）必须指向该提交，
否则停止，不能用另一分支的构建覆盖它。手工发布已有 tag 时，应从该 tag 运行；
新 tag 则由发布步骤绑定本次提交。远端查询失败不会被当作 tag 不存在。
版本以 `v` 或 `release-` 开头，后续只接受字母、数字、点、下划线和连字符，
并拒绝 `..`、尾随点及 `.lock` 后缀，以兼容原生安装目录与 Git ref。

### Release Artifacts

原生归档 `README.md` 与发布附件 `native-install-update.md` 由
`scripts/lib/native_guide.py` 从部署指南的原生安装节导出，不携带整本
checkout/云部署指南；两个产物共用同一段操作说明，避免归档内出现缺失的跨文档相对链接。

1. If WebUI code changed, confirm `service/base/internal/monitor/assets` was rebuilt from the current frontend source.
2. Confirm GHCR image owner and image names are correct for the release target.
3. Confirm the target tag format is correct:
   - `release-*`
   - `v*`
4. Confirm required GitHub repository secrets are present for any Cloudflare deploy you plan to run. See [docs/security.md#github-secrets](security.md#github-secrets).
5. Confirm deployment manifests contain no resolved secret values and pass checksum verification.
6. If the Local Server Web Console changed, confirm `/`, hashed assets, `#devices`, canonical login, Profile CRUD/CAS, and desktop/mobile layouts in a real browser against the final image.
7. Confirm the GitHub Release contains all six EasyProxy/`easyproxyctl` native
   archives, `config.example.yaml`, both service installers, `SHA256SUMS`, and
   `release-manifest.json`.
8. Verify `SHA256SUMS`, the GitHub build-provenance attestation, and an actual
   startup on Linux amd64, Linux arm64, and Windows amd64. Record Windows arm64
   as unsupported rather than silently omitting it.
9. Exercise update failure and rollback against disposable config and SQLite
   sentinels; verify both byte-for-byte after restoration.

### Upstream-Carried Modules

If `upstreams/*` changed, record whether each change is:

- an upstream sync import
- a local patch carried on top of upstream
- documentation or test-only

### Deployment Docs

If release behavior changed, update the corresponding docs:

- `README.md`
- `docs/deployment.md#quickstart`
- `deploy/service/base/README.md`
- `deploy/upstreams/misub/README.md`
- `deploy/upstreams/aggregator/README.md`
- `deploy/upstreams/ech-workers/README.md`
- `docs/deployment.md#service-base-config-distribution`
- `docs/local-server.md`
- `docs/release.md#release-notes-template`
- `docs/deployment.md#native-install-update`
- `deploy/nas/README.md`

### GitHub Actions Compatibility

1. Confirm workflows using JavaScript actions are still aligned with current GitHub runner requirements.
2. `deploy-cloudflare.yml` currently opts into Node 24 execution for JavaScript actions and uses `actions/setup-node@v6`.
3. If `cloudflare/wrangler-action` or other third-party actions publish a newer runtime-compatible major version, review and adopt it before a public release window.

<a id="release-contract"></a>

## Release Contract

Project: `EasyProxy`

Release class: `complex-multi-surface`

This repository follows the EasyAiMi release contract v1. The contract
standardizes the operator-facing behavior of GitHub Actions, GHCR images, and
blank-host local deployment. Project-specific build internals are allowed when
the public contract remains stable.

### Standard guarantees

- Manual publish workflows accept a standard `release_tag` override. Legacy `version` inputs remain supported where they already existed.
- Publish metadata writes both `release_tag` and `version` outputs when the workflow has a release metadata step.
- Runtime configuration is persistent local state and is not bundled into an
  image-publication workflow. Native releases include only an example config;
  installers preserve the operator's active config by default.
- GitHub Releases publish EasyProxy and `easyproxyctl` for Linux amd64, Linux
  arm64, and Windows amd64, plus `SHA256SUMS`, `release-manifest.json`, and a
  GitHub build-provenance attestation. Windows arm64 is explicitly unsupported.
- Local deployment starts from `deploy-host.ps1` and supports an empty host
  directory through recursive repository bootstrap plus GHCR images. Import
  codes remain an explicit advanced runtime-snapshot path, not a release
  prerequisite.
- Deployment logic must not depend on bind mounting `C:\Users\Public\nas_home\AI\GameEditor\<Project>` source trees.

### Workflows

| Component | Workflow | Tag inputs | Required artifacts | Required capabilities |
| --- | --- | --- | --- | --- |
| `service-and-workers` | `.github/workflows/publish-ghcr-images.yml` | `release_tag` | none | GHCR |
| `native-release` | `.github/workflows/publish-github-release.yml` | `release_tag` | six native archives, `SHA256SUMS`, `release-manifest.json` | GitHub Release, provenance attestation |

### Project-specific exceptions

- Multi-image publishing, Cloudflare deploy, and aggregator deploy stay project-specific.
- Runtime configuration is persistent local state and is not bundled into GHCR publication.

### Verification

Run this contract check from the repository root:

```powershell
python scripts/validate-release-contract.py
```

The check is intentionally textual. It verifies the workflow contract, artifact names, release tag aliases, the local deploy entrypoint, and this document without requiring live GitHub, GHCR, or R2 access.

<a id="release-notes-template"></a>

## Release Notes Template

Use this template for an authorized EasyProxy release. Fill in actual values;
delete sections that do not apply. A template does not authorize publishing.

### Identity

- Version/tag: `<tag>`
- Root commit: `<full-commit>`
- Changed upstream submodules: `<path: old-commit -> new-commit>`
- Published artifacts and image digests: `<name / URL / SHA256 or digest>`

### Changes

Summarize operator-visible changes by affected component, including runtime,
WebUI, cloud sources, deployment tooling, and compatibility changes.
Do not list unchanged components or historical migration milestones.

### Deployment And Upgrade

- Supported targets and relevant configuration changes: `<details>`
- Required operator actions: `<steps, or none>`
- Resource names: use the operator's topology or `<deployment>` placeholders,
  not maintainer-specific service names.
- Configuration/data preservation and rollback procedure: `<details>`
- Optional runtime snapshot/import-code changes: `<if applicable>`

Never include credentials, private import bundles, or secret-bearing URLs.
Link to the relevant current operator guide rather than duplicating it.

### Validation

- Actual validation commands/workflow runs: `<links and results>`
- Final artifact startup, API, and proxy tests: `<targets and results>`
- Data preservation and rollback checks: `<results>`
- Not tested or unsupported: `<explicit scope>`

Use [Release Checklist](release.md#release-checklist) and
[Release Contract](release.md#release-contract). Do not equate a build or workflow
success with unperformed runtime, deployment, or recovery validation.

### Breaking Changes And Known Limitations

List only changes and limitations that affect operators of this release.
Explain migration or mitigation steps without promising unapproved future work.
