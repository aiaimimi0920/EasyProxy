# Runtime Config Snapshot Distribution

EasyProxy can publish an **explicit snapshot** of the current local runtime
config to private Cloudflare R2. This is an advanced owner operation, not the
normal topology deployment path and not an automatic configuration authority.

## Authority and safety

- `deploy/service/base/config.yaml` (or the explicitly selected runtime file)
  remains the local runtime authority.
- `topology.yaml` supplies the deterministic R2 bucket name and environment
  variable references.
- Publication uploads the selected runtime file as-is; it does not render it
  from another root config.
- An ordinary deploy never downloads or overwrites a present runtime file.
- Enabling background R2 synchronization deliberately grants the remote object
  later-write authority and must be an explicit operator decision.
- Runtime snapshots may contain proxy credentials and connector tokens. Keep
  the bucket private and never upload the snapshot as a public GitHub artifact.

The former `Publish Service Base Config` workflow was removed because it mixed a
secret root config, rendering, publication, and release behavior. Backup and
restore workflows must use the lifecycle backup contract instead.

## Object layout

The local publisher writes:

```text
runtime/<deployment_name>/config.yaml
runtime/<deployment_name>/manifest.json
```

The distribution manifest records the account, bucket, endpoint, object keys,
size, content type, release version, and config fingerprint. It is distinct from
`deployment-manifest.json`, which records topology/resource/source versions.

## Local publication

Prerequisites:

1. a validated `topology.yaml`;
2. an existing runtime config;
3. the account and R2 secret references resolved in the process environment;
4. Python with `boto3`.

```powershell
$env:CLOUDFLARE_ACCOUNT_ID = '<account-id>'
$env:R2_ACCESS_KEY_ID = '<writer-key-id>'
$env:R2_SECRET_ACCESS_KEY = '<writer-secret>'

powershell -ExecutionPolicy Bypass -File .\scripts\publish-service-base-config.ps1 `
  -TopologyPath .\topology.yaml `
  -RuntimeConfigPath .\deploy\service\base\config.yaml `
  -ReleaseVersion <release-version>
```

Credentials are transferred to the Python uploader through the child process
environment, not command-line arguments. The script never prints their values.

## Bootstrap/import compatibility

Existing source-less runtimes can still consume an explicit bootstrap JSON or
an owner-provided import code:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\write-service-base-r2-bootstrap.ps1 `
  -ImportCode '<easyproxy-import-v1...>' `
  -OutputPath .\deploy\service\base\bootstrap\r2-bootstrap.json
```

Then deploy with one explicit bootstrap input:

```powershell
.\scripts\deploy-easyproxy.ps1 `
  -TopologyPath .\topology.yaml `
  -BootstrapFile .\deploy\service\base\bootstrap\r2-bootstrap.json
```

Do not provide both `-BootstrapFile` and `-ImportCode`. Never commit bootstrap
JSON, import codes, decrypted bundles, private keys, or downloaded runtime
configs.

## Update rule

Code updates preserve runtime data by default. A future update workflow may
create and verify a backup before deployment, but it must not silently treat a
missing snapshot as permission to replace the current config or create a second
state resource.
