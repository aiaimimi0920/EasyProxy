# Phase 06: Unified Root Config

## Goal

Provide one operator-facing root config file for the monorepo so users can see
and edit the main deployment/runtime inputs for each subproject in one place.

## Completed

- added root config template:
  - `config.example.yaml`
- added config helper library:
  - `scripts/lib/easyproxy-config.ps1`
- added config init and render entrypoints:
  - `scripts/init-config.ps1`
  - `scripts/render-derived-configs.ps1`
  - `scripts/render-derived-configs.py`
- integrated root config reads into:
  - `scripts/build-easyproxy-image.ps1`
  - `scripts/build-ech-workers-image.ps1`
  - `scripts/deploy-misub.ps1`
  - `scripts/deploy-ech-workers-cloudflare.ps1`
  - `scripts/deploy-aggregator.ps1`

## Rendered Outputs

The root config can now render:

- `deploy/service/base/config.yaml`
- `upstreams/misub/.env`
- `workers/ech-workers-cloudflare/.dev.vars`

## Validation

- `scripts/init-config.ps1` successfully generated a root `config.yaml`
- `scripts/render-derived-configs.ps1` successfully rendered:
  - `deploy/service/base/config.yaml`
  - `upstreams/misub/.env`
  - `workers/ech-workers-cloudflare/.dev.vars`
- the generated files match the expected target formats
