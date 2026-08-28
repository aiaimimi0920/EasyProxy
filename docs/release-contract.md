# Release Contract

Project: `EasyProxy`

Release class: `complex-multi-surface`

This repository follows the EasyAiMi release contract v1. The contract
standardizes the operator-facing behavior of GitHub Actions, GHCR images, and
blank-host local deployment. Project-specific build internals are allowed when
the public contract remains stable.

## Standard guarantees

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

## Workflows

| Component | Workflow | Tag inputs | Required artifacts | Required capabilities |
| --- | --- | --- | --- | --- |
| `service-and-workers` | `.github/workflows/publish-ghcr-images.yml` | `release_tag` | none | GHCR |
| `native-release` | `.github/workflows/publish-github-release.yml` | `release_tag` | six native archives, `SHA256SUMS`, `release-manifest.json` | GitHub Release, provenance attestation |

## Project-specific exceptions

- Multi-image publishing, Cloudflare deploy, and aggregator deploy stay project-specific.
- Runtime configuration is persistent local state and is not bundled into GHCR publication.

## Verification

Run this contract check from the repository root:

```powershell
python scripts/validate-release-contract.py
```

The check is intentionally textual. It verifies the workflow contract, artifact names, release tag aliases, the local deploy entrypoint, and this document without requiring live GitHub, GHCR, or R2 access.
