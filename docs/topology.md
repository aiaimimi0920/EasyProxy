# Topology Contract

`topology.yaml` is the tracked, non-secret description of one EasyProxy
deployment. Copy `topology.example.yaml` and validate it with:

```powershell
go -C tools/easyproxyctl run ./cmd/easyproxyctl topology validate --file ../../topology.yaml
```

## Authority Boundaries

- `topology.yaml` owns component enablement, deterministic cloud resource
  names, public endpoints, schedules, profiles, local access mode, and release
  channel.
- Environment/platform secret stores own every credential. Topology contains
  only environment variable names.
- Cloudflare APIs own live resource identity. A deployment manifest records the
  observed IDs and versions but does not replace API discovery.
- `deploy/service/base/config.yaml` owns EasyProxy runtime settings and WebUI
  persistence. Topology does not overwrite that file during ordinary updates.

The former root `config.yaml` and derived-config renderer were removed. Local
runtime initialization copies `deploy/service/base/config.template.yaml` once;
ordinary deploys and topology updates never overwrite the resulting file.

## Deterministic Names

Empty resource-name fields are derived from `deployment_name`:

- `<deployment>-misub-pages`
- `<deployment>-misub-d1`
- `<deployment>-ech-worker`
- `<deployment>-artifacts`

Names are normalized and hash-suffixed when truncation is required. Explicit
names must still satisfy the public resource-name contract.

## Update Safety

- Bootstrap reuses an exact-name resource and creates only a missing resource.
- Update requires every enabled resource to exist and never creates a
  replacement database or bucket.
- A topology/API identity mismatch stops the update.
- Manifests contain no resolved secret values and carry a checksum.

## Legacy Entry Point Decisions

| Former entry point | Decision |
| --- | --- |
| Root `config.yaml` / `config.example.yaml` | Removed as an application contract; an ignored local legacy file may remain only for manual migration reference. |
| `scripts/init-config.ps1` | Replaced by `init-topology.ps1` and `init-runtime-config.ps1`. |
| `scripts/render-derived-configs.*` | Deleted; ordinary deploys no longer regenerate runtime state. |
| `scripts/sync-github-deployment-settings.*` | Deleted; topology and platform APIs are the lifecycle contract. |
| `scripts/deploy-subproject.ps1`, `deploy-host.ps1` | Migrated to topology/runtime inputs and kept as thin operator adapters. |
| `publish-service-base-config.yml` | Deleted; an explicit local snapshot command remains, but is not an update authority. |

`easyproxyctl` owns topology validation, resource naming, reconciliation rules,
and deployment-manifest integrity. Provider scripts and workflows call that
contract; they must not implement a second naming or create-on-update policy.

## Deployment Manifest

Build a secret-free manifest after resources and immutable images have been
resolved. Source commits are collected from Git automatically; explicit
overrides support source-less release jobs:

```powershell
go -C tools/easyproxyctl run ./cmd/easyproxyctl manifest build `
  --topology ../../topology.yaml `
  --repo-root ../.. `
  --resource-id d1=<database-id> `
  --image easyproxy=ghcr.io/<owner>/<image>@sha256:<digest> `
  --output ../../deployment-manifest.json
```

Only enabled components/resources are emitted. The manifest records component
selection, root and recursive submodule commits, supplied provider IDs, supplied
immutable image references, workflow run, topology hash, and its own checksum.
