param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [string]$GitHubRepo = "",
    [string]$Workflow = "",
    [string]$Ref = "",
    [string]$SecretName = "",
    [string]$ConfigPathOverride = "",
    [switch]$SkipSecretUpdate,
    [switch]$SkipWorkflowTrigger
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-config.ps1")

Assert-EasyProxyCommand -Name "gh" -Hint "Install GitHub CLI and run gh auth login first."
Invoke-EasyProxyExternalCommand -FilePath "gh" -Arguments @("auth", "status") -FailureMessage "GitHub CLI is not authenticated"

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$aggregator = Get-EasyProxyConfigSection -Config $config -Name 'aggregator'

if ([string]::IsNullOrWhiteSpace($GitHubRepo)) {
    $GitHubRepo = [string](Get-EasyProxyConfigValue -Object $aggregator -Name 'githubRepo' -Default 'aiaimimi0920/aggregator')
}
if ([string]::IsNullOrWhiteSpace($Workflow)) {
    $Workflow = [string](Get-EasyProxyConfigValue -Object $aggregator -Name 'workflow' -Default 'process-r2.yaml')
}
if ([string]::IsNullOrWhiteSpace($Ref)) {
    $Ref = [string](Get-EasyProxyConfigValue -Object $aggregator -Name 'ref' -Default 'main')
}
if ([string]::IsNullOrWhiteSpace($SecretName)) {
    $SecretName = [string](Get-EasyProxyConfigValue -Object $aggregator -Name 'secretName' -Default 'SUBSCRIBE_CONF_JSON_B64')
}

$exportScript = Resolve-EasyProxyPath -Path "deploy/upstreams/aggregator/scripts/export_actions_config_base64.ps1"
$effectiveConfigPath = if ([string]::IsNullOrWhiteSpace($ConfigPathOverride)) {
    Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $aggregator -Name 'configPath' -Default 'deploy/upstreams/aggregator/config/config.actions.r2.json')
} else {
    Resolve-EasyProxyPath -Path $ConfigPathOverride
}

if (-not (Test-Path -LiteralPath $effectiveConfigPath)) {
    throw "Aggregator source config not found: $effectiveConfigPath"
}

$configContent = Get-Content -LiteralPath $effectiveConfigPath -Raw
if ($configContent -match '__KEY_PLACEHOLDER__' -or $configContent -match '__TOKEN_PLACEHOLDER__' -or $configContent -match 'PLACEHOLDER') {
    throw "Aggregator source config still contains placeholder values. Update $effectiveConfigPath before deploying."
}

if (-not $SkipSecretUpdate) {
    Write-Host "Encoding aggregator Actions config..." -ForegroundColor Cyan
    $encoded = powershell -ExecutionPolicy Bypass -File $exportScript -ConfigPath $effectiveConfigPath -NoNewline
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($encoded)) {
        throw "Failed to encode aggregator Actions config"
    }

    Write-Host "Updating GitHub secret $SecretName on $GitHubRepo..." -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand `
        -FilePath "gh" `
        -Arguments @("secret", "set", $SecretName, "--repo", $GitHubRepo, "--body", $encoded) `
        -FailureMessage "Failed to update aggregator GitHub secret"
}

if (-not $SkipWorkflowTrigger) {
    Write-Host "Triggering GitHub Actions workflow $Workflow on $GitHubRepo..." -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand `
        -FilePath "gh" `
        -Arguments @("workflow", "run", $Workflow, "--repo", $GitHubRepo, "--ref", $Ref) `
        -FailureMessage "Failed to trigger aggregator workflow"
}

Write-Host "Aggregator deployment workflow submitted."
