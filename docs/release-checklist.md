# Release Checklist

Use this checklist before publishing a tag or manually running the GHCR release workflow.

## Config And Secrets

1. Confirm `config.example.yaml` still contains placeholders only.
2. Confirm no live secrets were added to tracked files such as:
   - `config.yaml`
   - `deploy/service/base/config.yaml`
   - `upstreams/misub/.env`
   - `workers/ech-workers-cloudflare/.dev.vars`
3. Confirm `.gitignore` still excludes local runtime data, generated config, and Python caches.

## Validation

Run or confirm the latest successful CI result for:

```powershell
python -m unittest discover -s "tests" -p "test_*.py" -v
python -m unittest discover -s "upstreams/aggregator/tests" -p "test_*.py" -v

Set-Location service/base
go test ./internal/monitor
go test ./internal/boxmgr
go test ./internal/config
```

CI workflows:

- `.github/workflows/validate.yml`
- `.github/workflows/publish-ghcr-images.yml`
- `.github/workflows/deploy-cloudflare.yml`
- `.github/workflows/dispatch-aggregator.yml`

## Release Artifacts

1. If WebUI code changed, confirm `service/base/internal/monitor/assets` was rebuilt from the current frontend source.
2. Confirm GHCR image owner and image names are correct for the release target.
3. Confirm the target tag format is correct:
   - `release-*`
   - `v*`
4. Confirm required GitHub repository secrets are present for any Cloudflare deploy you plan to run. See [docs/github-secrets.md](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/docs/github-secrets.md).

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
