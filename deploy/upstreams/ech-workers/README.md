# ech-workers Runtime Packaging Note

Current runtime ownership:

- source code:
  - `upstreams/ech-workers`
- primary local host:
  - `EasyProxy`
- packaged binary path inside the EasyProxy image:
  - `/usr/local/bin/ech-workers`
- standalone container image name:
  - `ghcr.io/<owner>/ech-workers-monorepo:<release-tag>`

Operational rule:

- deploy `ech-workers` as part of the `EasyProxy` container image
- treat the bundled `EasyProxy` service image as the default contract for most users
- manage worker URL and access token through:
  - local `EasyProxy` connector config
  - or `MiSub` manifest connector sources
- optional standalone image build and publish:
  - `scripts/build-ech-workers-image.ps1`
  - `deploy/upstreams/ech-workers/scripts/publish-ghcr-ech-workers.ps1`
  - Dockerfile:
    - `deploy/upstreams/ech-workers/Dockerfile`

Recommended release shape:

- primary image for public operators:
  - `ghcr.io/<owner>/easy-proxy-monorepo-service:<release-tag>`
- optional specialized image for users who only need the local helper:
  - `ghcr.io/<owner>/ech-workers-monorepo:<release-tag>`

Root one-click publish entrypoints:

```powershell
# publish both core images
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project publish-core-images `
  -ReleaseTag release-20260427-001

# publish only the standalone worker image
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 `
  -Project publish-ech-workers-image `
  -ReleaseTag release-20260427-001
```

GitHub-hosted publish path:

- workflow:
  - `.github/workflows/publish-ghcr-images.yml`
- trigger:
  - push tag `release-*` or `v*`
  - or run `Publish GHCR Images` manually from the Actions tab with target
    `ech-workers`
