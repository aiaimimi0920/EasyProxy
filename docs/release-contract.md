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
  image-publication workflow or artifact.
- Local deployment starts from `deploy-host.ps1` and supports an empty host
  directory through recursive repository bootstrap plus GHCR images. Import
  codes remain an explicit advanced runtime-snapshot path, not a release
  prerequisite.
- Deployment logic must not depend on bind mounting `C:\Users\Public\nas_home\AI\GameEditor\<Project>` source trees.

## Workflows

| Component | Workflow | Tag inputs | Required artifacts | Required capabilities |
| --- | --- | --- | --- | --- |
| `service-and-workers` | `.github/workflows/publish-ghcr-images.yml` | `release_tag` | none | GHCR |

## Project-specific exceptions

- Multi-image publishing, GitHub Release, Cloudflare deploy, and aggregator deploy stay project-specific.
- Runtime configuration is persistent local state and is not bundled into GHCR publication.

## Verification

Run this contract check from the repository root:

```powershell
python scripts/validate-release-contract.py
```

The check is intentionally textual. It verifies the workflow contract, artifact names, release tag aliases, the local deploy entrypoint, and this document without requiring live GitHub, GHCR, or R2 access.
