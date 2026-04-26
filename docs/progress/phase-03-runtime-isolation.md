# Phase 03: Runtime Isolation

## Goal

Make the migrated `EasyProxy` monorepo safe to run in parallel with the legacy
source deployment on the same machine.

## Completed

- changed the monorepo deployment defaults to use:
  - container name `easy-proxy-monorepo-service`
  - local image name `easyproxy/easy-proxy-monorepo-service`
  - pool listener port `22323`
  - management port `29888`
  - multi-port base/range `25000-25500`
- changed the MiSub Docker/VPS default to use:
  - container name `misub-monorepo`
  - host port `18080`
- updated the monorepo deploy Dockerfile and helper scripts to use the new
  repository layout instead of the old multi-repo source layout
- changed the compose network name to `easyproxy-monorepo`

## Verification

- `deploy/service/base/docker-compose.yaml` renders successfully through
  `docker compose config`
- the main PowerShell helper scripts parse successfully
- the legacy source workspace remains unchanged

## Remaining Follow-Up

- broader cleanup of imported historical docs that still mention legacy names
  in historical sections
- optional root-level CI and validation entrypoints for the monorepo
