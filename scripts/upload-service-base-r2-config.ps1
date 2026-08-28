param(
    [string]$RuntimeConfigPath = 'deploy/service/base/config.yaml',
    [Parameter(Mandatory = $true)]
    [string]$AccountId,
    [Parameter(Mandatory = $true)]
    [string]$Bucket,
    [Parameter(Mandatory = $true)]
    [string]$ConfigObjectKey,
    [Parameter(Mandatory = $true)]
    [string]$ManifestObjectKey,
    [string]$Endpoint = '',
    [string]$ReleaseVersion = '',
    [string]$ManifestOutput = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

function Resolve-EasyProxyOutputPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path (Get-EasyProxyRepoRoot) $Path))
}

$resolvedConfigPath = Resolve-EasyProxyPath -Path $RuntimeConfigPath
Ensure-EasyProxyPathExists -Path $resolvedConfigPath -Message "Runtime config not found: $resolvedConfigPath"
if ([string]::IsNullOrWhiteSpace([string]$env:EASYPROXY_R2_ACCESS_KEY_ID) -or
    [string]::IsNullOrWhiteSpace([string]$env:EASYPROXY_R2_SECRET_ACCESS_KEY)) {
    throw 'EASYPROXY_R2_ACCESS_KEY_ID and EASYPROXY_R2_SECRET_ACCESS_KEY are required.'
}

Assert-EasyProxyCommand -Name 'python' -Hint 'Install Python 3 first.'
$pythonArgs = @(
    (Join-Path $PSScriptRoot 'upload-service-base-r2-config.py'),
    '--account-id', $AccountId,
    '--bucket', $Bucket,
    '--config-path', $resolvedConfigPath,
    '--config-object-key', $ConfigObjectKey,
    '--manifest-object-key', $ManifestObjectKey
)
if (-not [string]::IsNullOrWhiteSpace($Endpoint)) { $pythonArgs += @('--endpoint', $Endpoint) }
if (-not [string]::IsNullOrWhiteSpace($ReleaseVersion)) { $pythonArgs += @('--release-version', $ReleaseVersion) }
if (-not [string]::IsNullOrWhiteSpace($ManifestOutput)) {
    $pythonArgs += @('--manifest-output', (Resolve-EasyProxyOutputPath -Path $ManifestOutput))
}

& python @pythonArgs
if ($LASTEXITCODE -ne 0) {
    throw "R2 upload failed with exit code $LASTEXITCODE"
}
