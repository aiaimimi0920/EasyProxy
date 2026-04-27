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

## Aggregator Native Deployment

The native aggregator workflow now runs inside this repository:

- [deploy-aggregator.yml](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/.github/workflows/deploy-aggregator.yml)

Add these repository secrets before using that workflow:

| Secret | Purpose |
| --- | --- |
| `EASYPROXY_AGGREGATOR_GH_TOKEN` | GitHub token used by the aggregator GitHub crawler |
| `EASYPROXY_AGGREGATOR_R2_ACCESS_KEY_ID` | R2 write credential for published artifacts |
| `EASYPROXY_AGGREGATOR_R2_SECRET_ACCESS_KEY` | R2 write credential secret |
| `EASYPROXY_AGGREGATOR_R2_ACCOUNT_ID` | Cloudflare account ID for the target R2 bucket |
| `EASYPROXY_AGGREGATOR_SEED_SUB_KEY` | Replaces `__KEY_PLACEHOLDER__` in the tracked aggregator config template |
| `EASYPROXY_AGGREGATOR_SHARED_TOKEN` | Replaces `__TOKEN_PLACEHOLDER__` in the tracked aggregator config template |

Optional repository variables:

| Variable | Purpose |
| --- | --- |
| `EASYPROXY_AGGREGATOR_PUBLIC_BASE_URL` | Public base URL used for post-deploy artifact verification |
| `EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE` | Set to `true` to enable scheduled native aggregator runs |
| `EASYPROXY_AGGREGATOR_SKIP_ALIVE_CHECK` | Optional passthrough to the upstream runtime |
| `EASYPROXY_AGGREGATOR_SKIP_REMARK` | Optional passthrough to the upstream runtime |
| `EASYPROXY_AGGREGATOR_REACHABLE` | Optional passthrough to the upstream runtime |
| `EASYPROXY_AGGREGATOR_ENABLE_SPECIAL_PROTOCOLS` | Optional passthrough to the upstream runtime |
| `EASYPROXY_AGGREGATOR_LOG_LEVEL_DEBUG` | Optional passthrough to the upstream runtime |
