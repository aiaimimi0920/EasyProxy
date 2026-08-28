param(
    [string]$TopologyPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [string]$RuntimeConfigPath = (Join-Path $PSScriptRoot '..\deploy\service\base\config.yaml'),
    [string]$Endpoint = '',
    [string]$ReleaseVersion = '',
    [string]$ManifestOutput = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-topology.ps1')

$topology = Read-EasyProxyTopology -TopologyPath $TopologyPath
$resourceNames = Get-EasyProxyResourceNames -TopologyPath $TopologyPath
$accountId = Get-EasyProxyEnvironmentValue `
    -Reference ([string]$topology.cloudflare.account_id_env) `
    -Purpose 'R2 account selection'
$deploymentPrefix = "runtime/$($topology.deployment_name)"
$arguments = @(
    '-RuntimeConfigPath', $RuntimeConfigPath,
    '-AccountId', $accountId,
    '-Bucket', ([string]$resourceNames.r2_bucket),
    '-ConfigObjectKey', "$deploymentPrefix/config.yaml",
    '-ManifestObjectKey', "$deploymentPrefix/manifest.json"
)
if (-not [string]::IsNullOrWhiteSpace($Endpoint)) { $arguments += @('-Endpoint', $Endpoint) }
if (-not [string]::IsNullOrWhiteSpace($ReleaseVersion)) { $arguments += @('-ReleaseVersion', $ReleaseVersion) }
if (-not [string]::IsNullOrWhiteSpace($ManifestOutput)) { $arguments += @('-ManifestOutput', $ManifestOutput) }

$previousAccessKey = [Environment]::GetEnvironmentVariable('EASYPROXY_R2_ACCESS_KEY_ID', 'Process')
$previousSecretKey = [Environment]::GetEnvironmentVariable('EASYPROXY_R2_SECRET_ACCESS_KEY', 'Process')
try {
    $env:EASYPROXY_R2_ACCESS_KEY_ID = Get-EasyProxyEnvironmentValue `
        -Reference ([string]$topology.secrets.r2_access_key_id) `
        -Purpose 'R2 config publication'
    $env:EASYPROXY_R2_SECRET_ACCESS_KEY = Get-EasyProxyEnvironmentValue `
        -Reference ([string]$topology.secrets.r2_secret_access_key) `
        -Purpose 'R2 config publication'
    & (Join-Path $PSScriptRoot 'upload-service-base-r2-config.ps1') @arguments
    $uploadExitCode = $LASTEXITCODE
}
finally {
    [Environment]::SetEnvironmentVariable('EASYPROXY_R2_ACCESS_KEY_ID', $previousAccessKey, 'Process')
    [Environment]::SetEnvironmentVariable('EASYPROXY_R2_SECRET_ACCESS_KEY', $previousSecretKey, 'Process')
}
if ($uploadExitCode -ne 0) {
    throw "Failed to publish service/base runtime config with exit code $uploadExitCode"
}
