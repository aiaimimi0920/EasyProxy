# Root Host Deploy Script Standard

This repository must preserve a repository-root deployment entrypoint named:

- `deploy-host.ps1`

The contract for that file is:

1. It must be a single-file operator entrypoint.
2. It must remain usable even when an operator downloads only this one file
   from GitHub.
3. It must not require the operator to manually clone or checkout the full
   repository before the first run.
4. If the local repository layout is missing, the script must bootstrap a
   local repo cache automatically before invoking any canonical internal
   deploy scripts.
5. If the full repository is already present, the script should fast-path to
   the local checkout instead of downloading another copy.
6. README deployment examples must point operators at `deploy-host.ps1`
   first, not at a deeper helper script.
7. The single-file entrypoint may delegate to repository-local scripts only
   after it has ensured the required repo files are present locally.
8. When the product supports blank-host deployment from GHCR or import-code
   style bootstrap, the root script should prefer that path over local-image
   shortcuts.
9. The script should expose at least one no-side-effect self-check mode such
   as `-ResolveRepoOnly`, so future maintainers can verify the bootstrap path
   without launching the full runtime.

Future deploy changes must preserve this contract.

## Optional Runtime Snapshot Distribution

Import codes are an explicit advanced operator path for distributing a private
runtime snapshot, not a prerequisite or mandatory artifact of a normal release.
Native releases contain only example configuration; ordinary updates preserve
the active runtime configuration.

When an operator chooses this path, `deploy-host.ps1` accepts `-ImportCode` or
`-BootstrapFile` directly. The encrypted bundle may contain R2 locations,
manifest/object keys, read credentials, and sync metadata; keep it and its
private key out of Git and public release assets.

Follow [Runtime Config Distribution](service-base-config-distribution.md) for
snapshot publication, encryption, key derivation, and bootstrap details.
## Canonical Docker Naming

This repo must keep the short slug `easy-proxy` as the operator-facing
default for:

- Docker Compose project name
- default container name
- default network alias
- default local image name `easy-proxy/easy-proxy:local`
