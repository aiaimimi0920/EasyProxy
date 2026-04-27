param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
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

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$distribution = Get-EasyProxyConfigSection -Config $config -Name 'distribution'
$serviceBaseDistribution = Get-EasyProxyConfigSection -Config $distribution -Name 'serviceBase'

if ([string]::IsNullOrWhiteSpace($AccountId)) {
    $AccountId = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'accountId' -Default '')
}
if ([string]::IsNullOrWhiteSpace($Bucket)) {
    $Bucket = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'bucket' -Default '')
}
if ([string]::IsNullOrWhiteSpace($AccessKeyId)) {
    $AccessKeyId = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'accessKeyId' -Default '')
}
if ([string]::IsNullOrWhiteSpace($SecretAccessKey)) {
    $SecretAccessKey = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'secretAccessKey' -Default '')
}
if ([string]::IsNullOrWhiteSpace($ConfigObjectKey)) {
    $ConfigObjectKey = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'configObjectKey' -Default '')
}
if ([string]::IsNullOrWhiteSpace($ManifestObjectKey)) {
    $ManifestObjectKey = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'manifestObjectKey' -Default '')
}
if ([string]::IsNullOrWhiteSpace($Endpoint)) {
    $Endpoint = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'endpoint' -Default '')
}

& (Join-Path $PSScriptRoot 'upload-service-base-r2-config.ps1') `
    -ConfigPath $ConfigPath `
    -AccountId $AccountId `
    -Bucket $Bucket `
    -AccessKeyId $AccessKeyId `
    -SecretAccessKey $SecretAccessKey `
    -ConfigObjectKey $ConfigObjectKey `
    -ManifestObjectKey $ManifestObjectKey `
    -Endpoint $Endpoint `
    -ReleaseVersion $ReleaseVersion `
    -ManifestOutput $ManifestOutput

if ($LASTEXITCODE -ne 0) {
    throw "Failed to publish service/base config distribution with exit code $LASTEXITCODE"
}
