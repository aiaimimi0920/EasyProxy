# Phase 05: Root Operator Scripts

## Goal

Add root-level build and deploy entrypoints so monorepo operators do not have
to navigate into module-specific subdirectories to perform common deployment
tasks.

## Completed

- added shared script helpers in:
  - `scripts/lib/easyproxy-common.ps1`
- added root entrypoints:
  - `scripts/deploy-easyproxy.ps1`
  - `scripts/deploy-aggregator.ps1`
  - `scripts/deploy-misub.ps1`
  - `scripts/deploy-ech-workers-cloudflare.ps1`
  - `scripts/build-easyproxy-image.ps1`
  - `scripts/build-ech-workers-image.ps1`
- added a standalone ech-workers Dockerfile in:
  - `deploy/upstreams/ech-workers/Dockerfile`
- updated root and deploy docs so the new operator scripts are discoverable

## Validation

- all new PowerShell entrypoints parse successfully
- the standalone ech-workers image build was attempted through
  `scripts/build-ech-workers-image.ps1`
- the build reached Docker image resolution and then failed on an upstream
  Docker Hub EOF while fetching `golang:1.24`, so the current blocker is
  external registry access rather than script syntax

## Follow-Up

- rerun the local image builds once Docker Hub access is healthy
- optionally add a root `deploy-easyproxy.ps1` entrypoint later if full local
  compose deployment from the repo root becomes a common workflow
