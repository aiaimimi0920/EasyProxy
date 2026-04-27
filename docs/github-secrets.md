# GitHub Secrets

Use GitHub repository secrets for deployment credentials and runtime secrets that
must not live in committed files.

## Root Workflows

### `validate.yml`

This workflow does not require custom repository secrets.

### `publish-ghcr-images.yml`

This workflow uses the built-in `GITHUB_TOKEN` to push container images to
`ghcr.io/<repository-owner>/...`.

You do not need to add a separate PAT for the default GHCR workflow path unless
you want to publish across repositories or organizations with a custom token.

### `deploy-cloudflare.yml`

Add these repository secrets before using the manual Cloudflare deployment
workflow:

| Secret | Required For | Purpose |
| --- | --- | --- |
| `CLOUDFLARE_API_TOKEN` | MiSub Pages, ech-workers-cloudflare | Cloudflare deployment auth |
| `CLOUDFLARE_ACCOUNT_ID` | MiSub Pages, ech-workers-cloudflare | Cloudflare account targeting |
| `MISUB_ADMIN_PASSWORD` | MiSub Pages | MiSub admin login secret |
| `MISUB_COOKIE_SECRET` | MiSub Pages | MiSub session signing secret |
| `MISUB_MANIFEST_TOKEN` | MiSub Pages | MiSub manifest API token |
| `ECH_TOKEN` | ech-workers-cloudflare | Worker-side access token |

Notes:

- `MISUB_PUBLIC_URL` and `MISUB_CALLBACK_URL` are not secrets. They are
  currently tracked in
  [upstreams/misub/wrangler.jsonc](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/upstreams/misub/wrangler.jsonc).
- `deploy-cloudflare.yml` syncs MiSub secrets into the Cloudflare Pages project
  before deploying the latest build output.
- `deploy-cloudflare.yml` syncs `ECH_TOKEN` into the Worker secret store during
  deploy.

## Local Operator Scripts

These are not used by GitHub-hosted workflows, but local publishing can read:

| Variable | Purpose |
| --- | --- |
| `GHCR_USERNAME` | Local GHCR login override |
| `GHCR_TOKEN` | Local GHCR login token |

## Aggregator Secret Ownership

The tracked operator config points at an external repo:

- `aggregator.githubRepo`: `aiaimimi0920/aggregator`
- `aggregator.secretName`: `SUBSCRIBE_CONF_JSON_B64`

The new root workflow
[.github/workflows/dispatch-aggregator.yml](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/.github/workflows/dispatch-aggregator.yml)
updates that external repository secret and dispatches the target workflow.

Add this repository secret before using that workflow:

| Secret | Purpose |
| --- | --- |
| `AGGREGATOR_REPO_TOKEN` | GitHub token with permission to update repository secrets and dispatch workflows in the configured external aggregator repo |

The external repository still owns the `SUBSCRIBE_CONF_JSON_B64` secret itself.
This monorepo only pushes the refreshed value into that external secret store.
