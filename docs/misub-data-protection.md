# MiSub data protection and restore

## What is protected

Each `.age` backup is an encrypted `age` scrypt envelope containing a gzip/tar archive with exactly four entries:

- `database.sql`: full remote D1 export;
- `logical-backup.json`: complete logical sources, profiles, settings, Cron state, counts, and logical checksum;
- `deployment-manifest.json`: sealed topology/resource/source attestation;
- `backup-metadata.json`: source database name, ID, binding, schema version, row counts, row snapshot checksum, and per-file SHA-256 values.

The archive extractor rejects unknown names, duplicate entries, unsafe entry types, oversized entries, wrong passphrases, modified ciphertext, malformed metadata, and file checksum mismatches. Output files use private permissions and are never overwritten.

The preferred logical export is the authenticated MiSub API. For an upgrade from an older deployed MiSub that lacks this endpoint, `easyproxyctl` builds the same logical payload directly from the exact D1 storage rows. The D1 snapshot is taken before and after export; if it changes, the tool retries and refuses to publish an inconsistent backup after three attempts.

## Create a backup

Set the secret values named by topology:

```bash
export CLOUDFLARE_API_TOKEN='...'
export CLOUDFLARE_ACCOUNT_ID='...'
export MISUB_ADMIN_PASSWORD='...'
export EASYPROXY_BACKUP_PASSPHRASE='...'
```

Then run:

```bash
easyproxyctl cloud backup \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --wrangler-dir upstreams/misub \
  --base-url https://your-misub.example \
  --allow-direct-d1-fallback \
  --output backups/misub-$(date +%Y%m%d-%H%M%S).age
```

`--base-url` exercises authenticated full export and validates runtime resource identity. Direct-D1 fallback is disabled unless `--allow-direct-d1-fallback` is explicitly supplied; use it for protected upgrades from an older deployment that has no full-export endpoint. The resulting logical backup records `applicationVersion: d1-direct-export` so the fallback is auditable.

The manual GitHub equivalent is **Backup MiSub** (`backup-misub.yml`). It uploads only the encrypted archive. The passphrase is not stored in the artifact.

## Restore drill

Always run an isolated drill before considering production restore. Create a temporary D1 named with the protected database prefix:

```text
<production-database>-restore-drill-<unique-run-id>
```

Then run:

```bash
easyproxyctl cloud restore \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --wrangler-dir upstreams/misub \
  --input backups/misub.age \
  --target-database-name <drill-name> \
  --target-database-id <drill-id> \
  --confirm-database-id <drill-id> \
  --drill
```

The tool requires exactly one target matching the supplied name and ID, rejects the production D1 as a drill target, imports the SQL, and compares schema version, per-table row counts, and canonical database-row SHA-256 with backup metadata.

The **Restore MiSub** workflow defaults to `drill`. It creates a uniquely named D1, runs the restore verification, and deletes only that exact run-created D1 after rechecking its name and ID.

## Production restore

Production restore is intentionally harder:

- run it only through a protected operator session or the approval-gated `easyproxy-misub-restore` GitHub environment;
- provide the exact production D1 ID as confirmation;
- pass `--allow-production-restore`;
- provide a new, non-existing `--pre-restore-backup` output path;
- provide the current MiSub base URL.

Example:

```bash
easyproxyctl cloud restore \
  --topology topology.yaml \
  --manifest deployment-manifest.json \
  --wrangler-config upstreams/misub/wrangler.jsonc \
  --wrangler-dir upstreams/misub \
  --input backups/known-good.age \
  --target-database-name <production-name> \
  --target-database-id <production-id> \
  --confirm-database-id <production-id> \
  --allow-production-restore \
  --allow-direct-d1-fallback \
  --base-url https://your-misub.example \
  --pre-restore-backup backups/immediately-before-restore.age
```

The pre-restore backup must finish before SQL import starts. Retain both the original recovery archive and the automatic pre-restore archive. A restore command never creates or deletes a D1 database; only the drill workflow manages the temporary D1 it created itself.

## Failure rules

- Missing, ambiguous, or mismatched Cloudflare resources: stop.
- Manifest, topology, D1 binding, source identity, archive, or checksum mismatch: stop.
- Backup artifact upload failure: do not migrate or deploy.
- Database changes during backup: retry, then stop without publishing.
- Restore verification mismatch: treat the target as invalid; do not bind or deploy it.
- Lost passphrase: the encrypted backup is unrecoverable. Store it in an independent password manager.
