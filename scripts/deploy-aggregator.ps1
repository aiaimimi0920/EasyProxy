param(
    [string]$TopologyPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [string]$Workflow = "deploy-aggregator.yml",
    [string]$Ref = "main",
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
. (Join-Path $PSScriptRoot "lib\easyproxy-topology.ps1")

Invoke-EasyProxyExternalCommand -FilePath "gh" -Arguments @("auth", "status") -FailureMessage "GitHub CLI is not authenticated"

$topology = Read-EasyProxyTopology -TopologyPath $TopologyPath
if (-not [bool]$topology.components.aggregator) {
    throw 'Topology does not enable components.aggregator.'
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
