param(
    [string]$OutputPath = 'deploy/service/base/bootstrap/r2-bootstrap.json',
    [string]$ImportCode = '',
    [string]$ManifestPath = '',
    [string]$AccountId = '',
    [string]$Bucket = '',
    [string]$ManifestObjectKey = '',
    [string]$ConfigObjectKey = '',
    [string]$AccessKeyId = '',
    [string]$SecretAccessKey = '',
    [string]$Endpoint = '',
    [string]$ExpectedConfigSha256 = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

if (-not [string]::IsNullOrWhiteSpace($ImportCode)) {
    Assert-EasyProxyCommand -Name "python" -Hint "Install Python 3 first."
    $tempPath = Join-Path $env:TEMP ("easyproxy-import-code-" + [Guid]::NewGuid().ToString('N') + ".json")
    try {
        & python (Join-Path $PSScriptRoot 'easyproxy-import-code.py') inspect `
            --import-code $ImportCode `
            --output $tempPath
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to decode import code with exit code $LASTEXITCODE"
        }
        $payload = Get-Content -LiteralPath $tempPath -Raw | ConvertFrom-Json
        if (-not $AccountId) { $AccountId = [string]$payload.accountId }
        if (-not $Bucket) { $Bucket = [string]$payload.bucket }
        if (-not $Endpoint) { $Endpoint = [string]$payload.endpoint }
        if (-not $ManifestObjectKey) { $ManifestObjectKey = [string]$payload.manifestObjectKey }
        if (-not $AccessKeyId) { $AccessKeyId = [string]$payload.accessKeyId }
        if (-not $SecretAccessKey) { $SecretAccessKey = [string]$payload.secretAccessKey }
    } finally {
        Remove-Item -LiteralPath $tempPath -ErrorAction SilentlyContinue
    }
}

if (-not [string]::IsNullOrWhiteSpace($ManifestPath)) {
    $resolvedManifestPath = Resolve-EasyProxyPath -Path $ManifestPath
    if (-not (Test-Path -LiteralPath $resolvedManifestPath)) {
        throw "ManifestPath not found: $resolvedManifestPath"
    }

    $manifest = Get-Content -LiteralPath $resolvedManifestPath -Raw | ConvertFrom-Json
    if (-not $AccountId) { $AccountId = [string]$manifest.accountId }
    if (-not $Bucket) { $Bucket = [string]$manifest.bucket }
    if (-not $Endpoint) { $Endpoint = [string]$manifest.endpoint }
    if (-not $ManifestObjectKey) { $ManifestObjectKey = [string]$manifest.manifestObjectKey }
    if (-not $ConfigObjectKey) { $ConfigObjectKey = [string]$manifest.serviceBase.config.objectKey }
    if (-not $ExpectedConfigSha256) { $ExpectedConfigSha256 = [string]$manifest.serviceBase.config.sha256 }
}

foreach ($required in @(
    @{ Name = 'AccountId'; Value = $AccountId },
    @{ Name = 'Bucket'; Value = $Bucket },
    @{ Name = 'ManifestObjectKey or ConfigObjectKey'; Value = if ([string]::IsNullOrWhiteSpace($ManifestObjectKey)) { $ConfigObjectKey } else { $ManifestObjectKey } },
    @{ Name = 'AccessKeyId'; Value = $AccessKeyId },
    @{ Name = 'SecretAccessKey'; Value = $SecretAccessKey }
)) {
    if ([string]::IsNullOrWhiteSpace([string]$required.Value)) {
        throw "$($required.Name) is required."
    }
}

$bootstrap = [ordered]@{
    accountId = $AccountId
    endpoint = if ([string]::IsNullOrWhiteSpace($Endpoint)) {
        "https://$AccountId.r2.cloudflarestorage.com"
    } else {
        $Endpoint
    }
    bucket = $Bucket
    accessKeyId = $AccessKeyId
    secretAccessKey = $SecretAccessKey
    syncEnabled = $true
    syncIntervalSeconds = 7200
}

if (-not [string]::IsNullOrWhiteSpace($ManifestObjectKey)) {
    $bootstrap.manifestObjectKey = $ManifestObjectKey
}
if (-not [string]::IsNullOrWhiteSpace($ConfigObjectKey)) {
    $bootstrap.configObjectKey = $ConfigObjectKey
}
if (-not [string]::IsNullOrWhiteSpace($ExpectedConfigSha256)) {
    $bootstrap.expectedConfigSha256 = $ExpectedConfigSha256
}

$resolvedOutputPath = Join-Path (Get-EasyProxyRepoRoot) $OutputPath
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $resolvedOutputPath) | Out-Null
$bootstrap | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $resolvedOutputPath -Encoding UTF8
Write-Host "Bootstrap file written: $resolvedOutputPath"
