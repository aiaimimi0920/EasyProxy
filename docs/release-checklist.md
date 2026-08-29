# Release Checklist

Use this checklist before publishing a tag or manually running the GHCR release workflow.

## Config And Secrets

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

## Validation

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

## Release Artifacts

1. If WebUI code changed, confirm `service/base/internal/monitor/assets` was rebuilt from the current frontend source.
2. Confirm GHCR image owner and image names are correct for the release target.
3. Confirm the target tag format is correct:
   - `release-*`
   - `v*`
4. Confirm required GitHub repository secrets are present for any Cloudflare deploy you plan to run. See [docs/github-secrets.md](github-secrets.md).
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

## Upstream-Carried Modules

If `upstreams/*` changed, record whether each change is:

- an upstream sync import
- a local patch carried on top of upstream
- documentation or test-only

## Deployment Docs

If release behavior changed, update the corresponding docs:

- `README.md`
- `docs/quickstart.md`
- `deploy/service/base/README.md`
- `deploy/upstreams/misub/README.md`
- `deploy/upstreams/aggregator/README.md`
- `deploy/upstreams/ech-workers/README.md`
- `docs/service-base-config-distribution.md`
- `docs/local-server.md`
- `docs/release-notes-template.md`
- `docs/native-install-update.md`
- `deploy/nas/README.md`

## GitHub Actions Compatibility

1. Confirm workflows using JavaScript actions are still aligned with current GitHub runner requirements.
2. `deploy-cloudflare.yml` currently opts into Node 24 execution for JavaScript actions and uses `actions/setup-node@v6`.
3. If `cloudflare/wrangler-action` or other third-party actions publish a newer runtime-compatible major version, review and adopt it before a public release window.
