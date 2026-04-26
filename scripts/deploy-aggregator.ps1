param(
    [string]$GitHubRepo = "aiaimimi0920/aggregator",
    [string]$Workflow = "process-r2.yaml",
    [string]$Ref = "main",
    [switch]$SkipSecretUpdate,
    [switch]$SkipWorkflowTrigger
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")

Assert-EasyProxyCommand -Name "gh" -Hint "Install GitHub CLI and run gh auth login first."
Invoke-EasyProxyExternalCommand -FilePath "gh" -Arguments @("auth", "status") -FailureMessage "GitHub CLI is not authenticated"

$repoRoot = Get-EasyProxyRepoRoot
$exportScript = Resolve-EasyProxyPath -Path "deploy/upstreams/aggregator/scripts/export_actions_config_base64.ps1"
$configPath = Resolve-EasyProxyPath -Path "deploy/upstreams/aggregator/config/config.actions.r2.json"

if (-not $SkipSecretUpdate) {
    Write-Host "Encoding aggregator Actions config..." -ForegroundColor Cyan
    $encoded = powershell -ExecutionPolicy Bypass -File $exportScript -ConfigPath $configPath -NoNewline
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($encoded)) {
        throw "Failed to encode aggregator Actions config"
    }

    Write-Host "Updating GitHub secret SUBSCRIBE_CONF_JSON_B64 on $GitHubRepo..." -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand `
        -FilePath "gh" `
        -Arguments @("secret", "set", "SUBSCRIBE_CONF_JSON_B64", "--repo", $GitHubRepo, "--body", $encoded) `
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
