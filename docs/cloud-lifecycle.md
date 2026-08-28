# EasyProxy Cloudflare lifecycle

This document defines the operator contract for the root-owned Cloudflare lifecycle. The forked applications own their runtime code; `easyproxyctl`, root workflows, topology, manifests, and data protection are controlled by this repository.

## Authority and identity

1. `topology.yaml` is the desired-state authority. Secrets in topology are environment-variable names, never values.
2. `easyproxyctl cloud bootstrap` may create a missing Pages project or D1 database. It re-lists the resource after creation and requires exactly one exact-name result.
3. `easyproxyctl cloud update` only resolves existing resources. A missing or ambiguous result fails; update never creates a replacement.
4. `easyproxyctl manifest build` records topology checksum, source commits, and provider resource IDs.
5. `easyproxyctl cloud verify` verifies the sealed manifest and requires exactly one Wrangler D1 binding whose binding, database name, and database ID match the resolved state.

The MiSub D1 binding is always `MISUB_DB`. Names alone are not sufficient identity for a destructive or data-bearing operation.

## Bootstrap

Use bootstrap only for a new topology:

```bash
easyproxyctl cloud bootstrap \
  --topology topology.yaml \
  --state-output cloud-resources.json \
  --wrangler-dir upstreams/misub
```

After resolution:

1. Materialize Wrangler configuration with the exact Pages name, D1 name, D1 ID, and `MISUB_DB` binding.
2. Build a deployment manifest with `--resource-id pages=...` and `--resource-id d1=...`.
3. Run `cloud verify` against the materialized config.
4. Apply D1 migrations.
5. Deploy Pages and run semantic API verification.

Bootstrap is reentrant: a second bootstrap reuses the same exact resources and does not create duplicates.

## Update

The protected order is mandatory:

1. Resolve resources in update mode; missing resources fail closed.
2. Materialize and verify the exact runtime config and deployment manifest.
3. Create an encrypted backup and retain it outside the deployment workspace.
4. Apply expand-only D1 migrations.
5. Deploy application code.
6. Run authenticated runtime verification.

`.github/workflows/deploy-cloudflare.yml` implements this order. A failed backup or artifact upload prevents migration and deployment. The workflow does not treat arbitrary resource-creation errors as “already exists.”

## Verify

```bash
easyproxyctl cloud verify \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --state-output cloud-resources.json \
  --wrangler-dir upstreams/misub
```

Verification is local/control-plane attestation. The deploy workflow additionally checks public HTML, public config, authenticated login/settings, a non-empty manifest profile, and Cron status.

Runtime verifier secrets are read from environment variables. Never pass passwords, manifest tokens, access tokens, cookies, or backup passphrases as command-line arguments.

## Migrations

MiSub migrations live in `upstreams/misub/migrations` and are applied with:

```bash
npx wrangler d1 migrations apply <exact-database-name> --remote
```

Rules:

- migrations are expand-only during mixed-version operation;
- old code must continue to run against the expanded schema;
- no table or column removal is allowed in the same release as its replacement;
- destructive contract cleanup is a later, separately approved stable release;
- a populated update must have a retained encrypted backup before migration begins.

## GitHub configuration

Required repository secrets for MiSub lifecycle:

- `CLOUDFLARE_API_TOKEN`
- `CLOUDFLARE_ACCOUNT_ID`
- `MISUB_ADMIN_PASSWORD`
- `MISUB_COOKIE_SECRET`
- `MISUB_MANIFEST_TOKEN`
- `EASYPROXY_BACKUP_PASSPHRASE`

Required repository variables:

- `EASYPROXY_MISUB_PUBLIC_URL`
- `EASYPROXY_MISUB_CALLBACK_URL`
- optionally `EASYPROXY_MISUB_D1_DATABASE_NAME`
- optionally `EASYPROXY_MISUB_D1_DATABASE_BINDING` (must resolve to `MISUB_DB`)

Use a high-entropy backup passphrase independent of the MiSub admin password. Configure approval rules for the `easyproxy-misub-restore` GitHub environment before enabling production restore.
