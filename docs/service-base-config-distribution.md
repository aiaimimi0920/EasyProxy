# Service Base Config Distribution

`service/base` now supports a long-lived private config distribution path that
is suitable for public image releases and private operator bootstrap.

The model is intentionally split into three layers:

1. `publisher`
   - renders the effective `service/base` runtime config from the root
     `config.yaml`
   - uploads the rendered config and a manifest into private Cloudflare R2
2. `owner bootstrap`
   - generates an import code or bootstrap JSON that grants a client only the
     minimum read access required to fetch that config
3. `runtime`
   - downloads the config during container startup
   - periodically checks the manifest and refreshes the local runtime config if
     the fingerprint changes

## What Gets Published

The publishing workflow writes two objects into the private bucket:

- rendered runtime config
  - default key: `service-base/config.yaml`
- distribution manifest
  - default key: `service-base/manifest.json`

The manifest records:

- account id
- bucket
- endpoint
- object keys
- config hash / fingerprint
- release version

That manifest is the stable lookup point used by both bootstrap JSON and import
codes.

## GitHub Workflow

Primary workflow:

- [publish-service-base-config.yml](/C:/Users/Public/nas_home/AI/GameEditor/EasyProxy/.github/workflows/publish-service-base-config.yml)

What it does:

1. runs the repository validation preflight
2. decodes `EASYPROXY_ROOT_CONFIG_YAML_B64`
3. renders the effective `service/base` runtime config
4. uploads config + manifest into private R2
5. optionally emits an encrypted owner-only import-code artifact
6. verifies that bootstrap download works before the job is marked successful

## Local Publish Example

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\publish-service-base-config.ps1 `
  -ConfigPath .\config.yaml `
  -ReleaseVersion release-20260428-001
```

The script reads the `distribution.serviceBase` block from local
`config.yaml`, renders the effective `service/base` config, and uploads the
distribution objects to R2.

## Import Code Flow

Import codes are compact bootstrap payloads that contain:

- R2 account id / endpoint
- private bucket name
- manifest object key
- read-only access key id / secret access key
- sync settings
- release version

Generate a local owner keypair once:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\generate-import-code-keypair.ps1 `
  -PublicKeyOutput .\tmp\easyproxy_import_code_owner_public.txt `
  -PrivateKeyOutput .\tmp\easyproxy_import_code_owner_private.txt `
  -BundleOutput .\tmp\easyproxy_import_code_owner_keypair.json
```

After storing the public key in GitHub secret
`EASYPROXY_IMPORT_CODE_OWNER_PUBLIC_KEY`, the publish workflow can emit an
encrypted artifact that only the matching private key can decrypt.

Decrypt an owner-only artifact locally:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\decrypt-import-code.ps1 `
  -EncryptedFilePath .\service-base-import-code.encrypted.json `
  -PrivateKeyPath .\tmp\easyproxy_import_code_owner_private.txt `
  -OutputPath .\tmp\service-base-import-code.decrypted.json
```

## Bootstrap JSON Flow

Some operators prefer to ship an explicit bootstrap JSON file instead of
passing an import code through environment variables.

Build a bootstrap JSON from an import code:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\write-service-base-r2-bootstrap.ps1 `
  -ImportCode "<easyproxy-import-v1...>" `
  -OutputPath .\deploy\service\base\bootstrap\r2-bootstrap.json
```

Build a bootstrap JSON from a manifest plus explicit read credentials:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\write-service-base-r2-bootstrap.ps1 `
  -ManifestPath .\service-base-r2-config-manifest.json `
  -AccessKeyId "<read-access-key-id>" `
  -SecretAccessKey "<read-secret-access-key>" `
  -OutputPath .\deploy\service\base\bootstrap\r2-bootstrap.json
```

## Runtime Consumption

The image entrypoint supports two bootstrap modes:

1. bootstrap file mounted into the container
2. import code provided through environment variable

### Import Code Example

```powershell
docker run --rm `
  -p 29888:29888 `
  -e EASY_PROXY_IMPORT_CODE="<easyproxy-import-v1...>" `
  ghcr.io/aiaimimi0920/easy-proxy-monorepo-service:<release-tag>
```

### Bootstrap File Example

```powershell
docker run --rm `
  -p 29888:29888 `
  -v ${PWD}\deploy\service\base\bootstrap\r2-bootstrap.json:/etc/easy-proxy/bootstrap/r2-bootstrap.json:ro `
  ghcr.io/aiaimimi0920/easy-proxy-monorepo-service:<release-tag>
```

At startup the image will:

1. inspect the bootstrap source
2. download the manifest and rendered config
3. verify the expected hash when present
4. write `/etc/easy-proxy/config.yaml`
5. start `easy-proxy`

After startup a sync loop continues to watch the manifest fingerprint and
refreshes the runtime config if a newer release is published.

## Security Notes

- The bucket is private. Access is controlled by purpose-built R2 tokens.
- Publish credentials and runtime read credentials are separate.
- The encrypted import-code artifact is optional, but recommended for owner-only
  handling.
- Local `config.yaml`, bootstrap files, and generated runtime config remain
  ignored by Git.
- If you rotate read credentials, regenerate import codes / bootstrap JSON and
  republish the config distribution.
