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

### `publish-service-base-config.yml`

Add these repository secrets before using the private service/base config
distribution workflow:

| Secret | Purpose |
| --- | --- |
| `EASYPROXY_ROOT_CONFIG_YAML_B64` | Base64-encoded root `config.yaml` used to render the final `service/base` runtime config |
| `EASYPROXY_R2_CONFIG_ACCOUNT_ID` | Cloudflare account id that owns the private R2 bucket |
| `EASYPROXY_R2_CONFIG_BUCKET` | Private R2 bucket name for the service/base runtime config |
| `EASYPROXY_R2_CONFIG_ENDPOINT` | Optional explicit R2 S3 endpoint |
| `EASYPROXY_R2_CONFIG_CONFIG_OBJECT_KEY` | Object key for rendered `service/base` `config.yaml` |
| `EASYPROXY_R2_CONFIG_MANIFEST_OBJECT_KEY` | Object key for the EasyProxy service-base distribution manifest |
| `EASYPROXY_R2_CONFIG_UPLOAD_ACCESS_KEY_ID` | R2 upload access key id used by GitHub Actions |
| `EASYPROXY_R2_CONFIG_UPLOAD_SECRET_ACCESS_KEY` | R2 upload secret access key used by GitHub Actions |
| `EASYPROXY_R2_CONFIG_READ_ACCESS_KEY_ID` | Client-side R2 read-only access key id |
| `EASYPROXY_R2_CONFIG_READ_SECRET_ACCESS_KEY` | Client-side R2 read-only secret access key |

Optional secret for encrypted owner-only bootstrap artifacts:

| Secret | Purpose |
| --- | --- |
| `EASYPROXY_IMPORT_CODE_OWNER_PUBLIC_KEY` | Owner public key used to generate encrypted import-code artifacts |

### `deploy-cloudflare.yml`

Add these repository secrets before using the manual Cloudflare deployment
workflow:

| Secret | Required For | Purpose |
| --- | --- | --- |
| `CLOUDFLARE_API_TOKEN` | MiSub Pages, ech-workers-cloudflare | Preferred Cloudflare deployment auth |
| `CLOUDFLARE_AUTH_EMAIL` | MiSub Pages, ech-workers-cloudflare | Fallback auth email when using Cloudflare Global API Key |
| `CLOUDFLARE_GLOBAL_API_KEY` | MiSub Pages, ech-workers-cloudflare | Fallback deployment auth when API token is unavailable |
| `CLOUDFLARE_ACCOUNT_ID` | MiSub Pages, ech-workers-cloudflare | Cloudflare account targeting |
| `MISUB_ADMIN_PASSWORD` | MiSub Pages | MiSub admin login secret |
| `MISUB_COOKIE_SECRET` | MiSub Pages | MiSub session signing secret |
| `MISUB_MANIFEST_TOKEN` | MiSub Pages | MiSub manifest API token |
| `ECH_TOKEN` | ech-workers-cloudflare | Worker-side access token |

Notes:

- `deploy-cloudflare.yml` accepts either:
  - `CLOUDFLARE_API_TOKEN`
  - or `CLOUDFLARE_AUTH_EMAIL` + `CLOUDFLARE_GLOBAL_API_KEY`

- `MISUB_PUBLIC_URL` and `MISUB_CALLBACK_URL` are not secrets. They are
  expected as repository variables:
  - `EASYPROXY_MISUB_PUBLIC_URL`
  - `EASYPROXY_MISUB_CALLBACK_URL`
- MiSub D1 resolution also expects repository variables:
  - `EASYPROXY_MISUB_D1_DATABASE_NAME`
  - `EASYPROXY_MISUB_D1_DATABASE_BINDING`
  - `EASYPROXY_MISUB_MANIFEST_PROFILE_ID`
- ech-workers-cloudflare verification expects repository variable:
  - `EASYPROXY_ECH_WORKER_PUBLIC_URL`
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

Optional repository secrets:

| Secret | Purpose |
| --- | --- |
| `EASYPROXY_AGGREGATOR_SEED_SUB_KEY` | Enables the tracked disabled seed-sub template entries when present |
| `EASYPROXY_AGGREGATOR_SHARED_TOKEN` | Enables the tracked Issue #91 shared fallback seed when present |

If the placeholder-backed aggregator secrets are missing, the native
materialization step disables only those affected seed entries instead of
failing the whole deployment.
