param(
    [string]$ConfigPath = 'config.yaml',
    [string]$AccountId = '',
    [string]$Bucket = '',
    [string]$AccessKeyId = '',
    [string]$SecretAccessKey = '',
    [string]$ConfigObjectKey = '',
    [string]$ManifestObjectKey = '',
    [string]$Endpoint = '',
    [string]$ReleaseVersion = '',
    [string]$ManifestOutput = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-config.ps1')

function Resolve-EasyProxyOutputPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Output path must not be empty."
    }

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }

    return [System.IO.Path]::GetFullPath((Join-Path (Get-EasyProxyRepoRoot) $Path))
}

foreach ($required in @(
    @{ Name = 'AccountId'; Value = $AccountId },
    @{ Name = 'Bucket'; Value = $Bucket },
    @{ Name = 'AccessKeyId'; Value = $AccessKeyId },
    @{ Name = 'SecretAccessKey'; Value = $SecretAccessKey },
    @{ Name = 'ConfigObjectKey'; Value = $ConfigObjectKey },
    @{ Name = 'ManifestObjectKey'; Value = $ManifestObjectKey }
)) {
    if ([string]::IsNullOrWhiteSpace([string]$required.Value)) {
        throw "$($required.Name) is required."
    }
}

$resolvedConfigPath = Resolve-EasyProxyPath -Path $ConfigPath
$tempRoot = [string]$env:TEMP
if ([string]::IsNullOrWhiteSpace($tempRoot)) {
    $tempRoot = [System.IO.Path]::GetTempPath()
}
if ([string]::IsNullOrWhiteSpace($tempRoot)) {
    throw "Unable to resolve a temporary directory for rendering service/base config."
}
$renderServiceOutput = Join-Path $tempRoot ("easyproxy-service-base-runtime-" + [Guid]::NewGuid().ToString("N") + ".yaml")

try {
    & (Join-Path $PSScriptRoot 'render-derived-configs.ps1') `
        -ConfigPath $resolvedConfigPath `
        -ServiceBase `
        -ServiceOutput $renderServiceOutput
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to render service/base runtime config with exit code $LASTEXITCODE"
    }

    Assert-EasyProxyCommand -Name "python" -Hint "Install Python 3 first."
    $pythonScript = Join-Path $PSScriptRoot 'upload-service-base-r2-config.py'
    $pythonArgs = @(
        $pythonScript,
        '--account-id', $AccountId,
        '--bucket', $Bucket,
        '--access-key-id', $AccessKeyId,
        '--secret-access-key', $SecretAccessKey,
        '--config-path', $renderServiceOutput,
        '--config-object-key', $ConfigObjectKey,
        '--manifest-object-key', $ManifestObjectKey
    )
    if (-not [string]::IsNullOrWhiteSpace($Endpoint)) {
        $pythonArgs += @('--endpoint', $Endpoint)
    }
    if (-not [string]::IsNullOrWhiteSpace($ReleaseVersion)) {
        $pythonArgs += @('--release-version', $ReleaseVersion)
    }
    if (-not [string]::IsNullOrWhiteSpace($ManifestOutput)) {
        $pythonArgs += @('--manifest-output', (Resolve-EasyProxyOutputPath -Path $ManifestOutput))
    }

    & python @pythonArgs
    if ($LASTEXITCODE -ne 0) {
        throw "R2 upload failed with exit code $LASTEXITCODE"
    }
}
finally {
    Remove-Item -LiteralPath $renderServiceOutput -ErrorAction SilentlyContinue
}
