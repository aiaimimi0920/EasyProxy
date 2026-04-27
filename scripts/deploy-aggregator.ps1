param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [string]$Workflow = "",
    [string]$Ref = "",
    [ValidateSet("bootstrap", "update")]
    [string]$DeploymentMode = "update",
    [bool]$RunVerification = $true,
    [bool]$ForceDeploy = $false,
    [switch]$SkipSecretUpdate,
    [switch]$SkipWorkflowTrigger
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-config.ps1")

Invoke-EasyProxyExternalCommand -FilePath "gh" -Arguments @("auth", "status") -FailureMessage "GitHub CLI is not authenticated"

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$aggregator = Get-EasyProxyConfigSection -Config $config -Name 'aggregator'

if ([string]::IsNullOrWhiteSpace($Workflow)) {
    $Workflow = [string](Get-EasyProxyConfigValue -Object $aggregator -Name 'workflowFile' -Default 'deploy-aggregator.yml')
}
if ([string]::IsNullOrWhiteSpace($Ref)) {
    $Ref = [string](Get-EasyProxyConfigValue -Object $aggregator -Name 'ref' -Default 'main')
}

if (-not $SkipSecretUpdate) {
    Write-Host "Aggregator now uses this repository's native workflow and GitHub Secrets." -ForegroundColor Yellow
    Write-Host "Skipping legacy secret push step because external repository dispatch is retired." -ForegroundColor Yellow
}

if (-not $SkipWorkflowTrigger) {
    Write-Host "Triggering native GitHub Actions workflow $Workflow on the current repository..." -ForegroundColor Cyan
    $runVerificationValue = if ($RunVerification) { "true" } else { "false" }
    $forceDeployValue = if ($ForceDeploy) { "true" } else { "false" }
    Invoke-EasyProxyExternalCommand `
        -FilePath "gh" `
        -Arguments @(
            "workflow", "run", $Workflow,
            "--ref", $Ref,
            "-f", "deployment_mode=$DeploymentMode",
            "-f", "run_verification=$runVerificationValue",
            "-f", "force_deploy=$forceDeployValue"
        ) `
        -FailureMessage "Failed to trigger native aggregator workflow"
}

Write-Host "Aggregator deployment workflow submitted."
