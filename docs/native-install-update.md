# Native install, update, and rollback

## Support matrix

| Product | Platform | Status |
| --- | --- | --- |
| EasyProxy service | Linux amd64 | Supported release archive and systemd installer |
| EasyProxy service | Linux arm64 | Supported release archive and systemd installer |
| EasyProxy service | Windows amd64 | Supported release archive and native Windows Service installer |
| EasyProxy service | Windows arm64 | Unsupported; no artifact is published |
| `easyproxyctl` | same supported targets | Separate matching archive |

Native packages support the Local Server, proxy pool, Web console, and
management API. Windows native transparent gateway mode is not supported.
Linux transparent gateway deployment remains the advanced Debian/Docker path.

## Stable filesystem contract

- Linux programs: `/opt/easyproxy/releases/<version>` with `current` and
  `previous` symlinks.
- Linux config: `/etc/easyproxy/config.yaml`.
- Linux state: `/var/lib/easyproxy`.
- Windows programs: `%ProgramFiles%\EasyProxy\releases\<version>`.
- Windows config and state: `%ProgramData%\EasyProxy`.

An update never replaces an existing config unless `--replace-config` on Linux
or `-ReplaceConfig` on Windows is explicitly supplied. Both installers stop the
service before copying config and SQLite state into a timestamped backup.

## Linux

```sh
owner_repo='<OWNER>/<REPOSITORY>'
version='<RELEASE_TAG>'
base="https://github.com/${owner_repo}/releases/download/${version}"
curl -fLO "${base}/install.sh"
curl -fLO "${base}/SHA256SUMS"
grep '  install.sh$' SHA256SUMS | sha256sum -c -
sudo sh install.sh --version "${version}" --repository "${owner_repo}"
# Explicit config replacement only when intended:
sudo sh install.sh --version "${version}" --repository "${owner_repo}" --replace-config
# Restore both program pointer and data snapshot:
sudo sh install.sh --rollback /var/lib/easyproxy/backups/before-<RELEASE_TAG>-...
```

The installer downloads the platform archive plus `SHA256SUMS`, verifies the
hash, installs the systemd unit, enables startup, and requires the service to be
active. `--archive` supports an already downloaded verified package.

## Windows

Run an elevated PowerShell prompt:

```powershell
$repository = '<OWNER>/<REPOSITORY>'
$version = '<RELEASE_TAG>'
$base = "https://github.com/$repository/releases/download/$version"
Invoke-WebRequest "$base/install-service.ps1" -OutFile .\install-service.ps1
Invoke-WebRequest "$base/SHA256SUMS" -OutFile .\SHA256SUMS
$expected = (((Get-Content .\SHA256SUMS | Where-Object { $_ -match '\s+install-service\.ps1$' }) -split '\s+')[0]).ToLowerInvariant()
$actual = (Get-FileHash .\install-service.ps1 -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw 'installer checksum mismatch' }
.\install-service.ps1 -Version $version -Repository $repository
.\install-service.ps1 -Version $version -Repository $repository -ReplaceConfig
.\install-service.ps1 -Rollback -BackupPath 'C:\ProgramData\EasyProxy\backups\before-<RELEASE_TAG>-...'
```

The installer verifies `SHA256SUMS`, registers a real SCM service with an
absolute config path, enables automatic startup, opens TCP ports 22323/29888,
and waits for the service to enter `Running`. Keep a copy of the selected
backup until the updated service has passed application-level checks.
