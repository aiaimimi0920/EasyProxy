# Secrets And Permissions

Topology stores environment variable names only. Resolved values belong in a
GitHub Environment or the local process environment and must never enter Git,
artifacts, manifests, summaries, or command arguments that are logged.

| Topology reference | Recommended GitHub location | Required for | Minimum purpose |
| --- | --- | --- | --- |
| `CLOUDFLARE_API_TOKEN` | Environment secret | cloud components | Pages, D1, Workers, and configured R2 resources |
| `CLOUDFLARE_ACCOUNT_ID` | Repository variable | cloud components | select one exact account; never pick the first account implicitly |
| `CLOUDFLARE_ZONE_ID` | Repository variable | custom domains | select the exact DNS zone |
| `CLOUDFLARE_DNS_TOKEN` | Environment secret | managed custom DNS | DNS edit for the selected zone only |
| `MISUB_ADMIN_PASSWORD` | Environment secret | MiSub | administrative login |
| `MISUB_COOKIE_SECRET` | Environment secret | MiSub | stable session signing key |
| `MISUB_MANIFEST_TOKEN` | Environment secret | MiSub/EasyProxy | machine manifest authentication |
| `MISUB_CRON_SECRET` | Environment secret | MiSub cron | cron endpoint authentication |
| `EASYPROXY_BACKUP_PASSPHRASE` | Environment secret | MiSub backup/restore | independent age archive encryption |
| `ECH_TOKEN` | Environment secret | ECH Worker | WebSocket connector authentication |
| `ECH_TOKEN_NEXT` | rotation Environment secret | ECH rotation | candidate token; ordinary update never reads it |
| `EASYPROXY_REPOSITORY_ADMIN_TOKEN` | rotation Environment secret | ECH rotation | repository Secrets write only |
| `EASYPROXY_MANAGEMENT_PASSWORD` | rotation Environment secret | ECH rotation | dedicated EasyProxy validator authentication |
| `R2_ACCESS_KEY_ID` | Environment secret | Aggregator/backup | selected bucket access key ID |
| `R2_SECRET_ACCESS_KEY` | Environment secret | Aggregator/backup | selected bucket secret key |

Use separate DNS and deployment tokens when custom DNS is enabled. Production
deployments use a protected GitHub Environment; pull-request validation receives
no production secrets. Restore and token rotation require manual approval.

Pass secret values to child processes only through inherited environment
variables or standard input. Do not expose them as PowerShell or executable
arguments because process listings and test captures can persist argv. Scripts
that temporarily map topology references to conventional environment names must
restore the previous process environment in `finally` blocks.

Keep `EASYPROXY_REPOSITORY_ADMIN_TOKEN` out of ordinary deployment jobs. It is
needed because the workflow's default `GITHUB_TOKEN` cannot be treated as a
general repository-secret administrator. Require manual approval on the
`easyproxy-ech-rotation` Environment.
