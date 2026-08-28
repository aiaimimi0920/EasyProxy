param(
    [string]$TopologyPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [string]$RuntimeConfigPath = (Join-Path $PSScriptRoot '..\deploy\service\base\config.yaml'),
    [string]$ImportCode = '',
    [string]$BootstrapFile = '',
    [switch]$NoBuild,
    [switch]$FromGhcr,
    [string]$Image = '',
    [string]$ReleaseTag = '',
    [string]$GhcrOwner = '',
    [switch]$SkipPull,
    [string]$ContainerName = '',
    [string]$PoolPortBinding = '',
    [string]$ManagementPortBinding = '',
    [string]$MultiPortBinding = '',
    [string]$NetworkAlias = '',
    [string]$ComposeProjectName = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-ghcr.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-topology.ps1')

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$topology = Read-EasyProxyTopology -TopologyPath $TopologyPath
if (-not [bool]$topology.components.local_easyproxy) {
    throw 'Topology does not enable components.local_easyproxy.'
}
if ([string]$topology.local.install_mode -ne 'docker') {
    throw "This wrapper requires local.install_mode: docker."
}
$composeFile = Resolve-EasyProxyPath -Path 'deploy/service/base/docker-compose.yaml'
$serviceOutput = Resolve-EasyProxyPath -Path $RuntimeConfigPath -AllowMissing
$networkName = "$($topology.deployment_name)-network"
$useGhcrDeploy = $FromGhcr -or -not [string]::IsNullOrWhiteSpace($Image) -or -not [string]::IsNullOrWhiteSpace($ReleaseTag)
$bootstrapPath = Resolve-EasyProxyPath -Path 'deploy/service/base/bootstrap/r2-bootstrap.json' -AllowMissing
$bootstrapStatePath = Resolve-EasyProxyPath -Path 'deploy/service/base/bootstrap/.import-state.json' -AllowMissing

Ensure-EasyProxyPathExists -Path $composeFile -Message "Missing EasyProxy docker compose file: $composeFile"

if (-not [string]::IsNullOrWhiteSpace($ImportCode) -and -not [string]::IsNullOrWhiteSpace($BootstrapFile)) {
    throw 'Specify either ImportCode or BootstrapFile, not both.'
}

$shouldBootstrapFromImport = -not [string]::IsNullOrWhiteSpace($ImportCode) -or -not [string]::IsNullOrWhiteSpace($BootstrapFile)

if ($shouldBootstrapFromImport) {
    $bootstrapDir = Split-Path -Parent $bootstrapPath
    New-Item -ItemType Directory -Force -Path $bootstrapDir | Out-Null
    if (-not [string]::IsNullOrWhiteSpace($ImportCode)) {
        Invoke-EasyProxyExternalCommand -FilePath 'powershell' -Arguments @(
            '-ExecutionPolicy', 'Bypass',
            '-File', (Join-Path $PSScriptRoot 'write-service-base-r2-bootstrap.ps1'),
            '-ImportCode', $ImportCode,
            '-OutputPath', $bootstrapPath
        ) -FailureMessage 'Failed to materialize EasyProxy bootstrap from import code'
    } else {
        $resolvedBootstrapFile = Resolve-EasyProxyPath -Path $BootstrapFile
        Ensure-EasyProxyPathExists -Path $resolvedBootstrapFile -Message "Bootstrap file not found: $resolvedBootstrapFile"
        Copy-Item -LiteralPath $resolvedBootstrapFile -Destination $bootstrapPath -Force
    }

    Invoke-EasyProxyExternalCommand -FilePath 'python' -Arguments @(
        (Join-Path $PSScriptRoot '..\deploy\service\base\bootstrap-service-config.py'),
        '--bootstrap-path', $bootstrapPath,
        '--config-path', $serviceOutput,
        '--state-path', $bootstrapStatePath
    ) -FailureMessage 'Failed to bootstrap EasyProxy runtime config from R2'
}
elseif (-not (Test-Path -LiteralPath $serviceOutput)) {
    $template = Resolve-EasyProxyPath -Path 'deploy/service/base/config.template.yaml'
    $serviceOutputParent = Split-Path -Parent $serviceOutput
    if (-not [string]::IsNullOrWhiteSpace($serviceOutputParent)) {
        New-Item -ItemType Directory -Force -Path $serviceOutputParent | Out-Null
    }
    Copy-Item -LiteralPath $template -Destination $serviceOutput
    Write-Host "Initialized runtime config once: $serviceOutput" -ForegroundColor Yellow
}

Ensure-EasyProxyPathExists -Path $serviceOutput -Message "Missing rendered EasyProxy runtime config: $serviceOutput"

if ($useGhcrDeploy) {
    if ([string]::IsNullOrWhiteSpace($Image)) {
        if ([string]::IsNullOrWhiteSpace($ReleaseTag)) {
            throw "GHCR deployment requires -Image or -ReleaseTag."
        }

        if ([string]::IsNullOrWhiteSpace($GhcrOwner)) {
            $GhcrOwner = [string]$env:GITHUB_REPOSITORY_OWNER
        }
        Assert-EasyProxyGhcrOwnerIsSafe -Owner $GhcrOwner -SourceDescription "GHCR owner"

        $serviceImageName = 'easy-proxy-monorepo-service'
        $Image = "ghcr.io/$GhcrOwner/${serviceImageName}:$ReleaseTag"
    }

    $runtimeRoot = Split-Path -Parent $composeFile
    $deployGhcrScript = Join-Path $PSScriptRoot '..\deploy\service\base\scripts\deploy-ghcr-runtime.ps1'
    $ghcrArgs = @(
        '-ExecutionPolicy', 'Bypass',
        '-File', $deployGhcrScript,
        '-ConfigPath', $serviceOutput,
        '-Image', $Image,
        '-RuntimeRoot', $runtimeRoot,
        '-NetworkName', $networkName,
        '-ComposeSourcePath', $composeFile
    )
    if (-not [string]::IsNullOrWhiteSpace($ContainerName)) { $ghcrArgs += @('-ContainerName', $ContainerName) }
    if (-not [string]::IsNullOrWhiteSpace($PoolPortBinding)) { $ghcrArgs += @('-PoolPortBinding', $PoolPortBinding) }
    if (-not [string]::IsNullOrWhiteSpace($ManagementPortBinding)) { $ghcrArgs += @('-ManagementPortBinding', $ManagementPortBinding) }
    if (-not [string]::IsNullOrWhiteSpace($MultiPortBinding)) { $ghcrArgs += @('-MultiPortBinding', $MultiPortBinding) }
    if (-not [string]::IsNullOrWhiteSpace($NetworkAlias)) { $ghcrArgs += @('-NetworkAlias', $NetworkAlias) }
    if (-not [string]::IsNullOrWhiteSpace($ComposeProjectName)) { $ghcrArgs += @('-ComposeProjectName', $ComposeProjectName) }
    if ($SkipPull) {
        $ghcrArgs += '-SkipPull'
    }

    Write-Host "Deploying EasyProxy from GHCR image: $Image" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath 'powershell' -Arguments $ghcrArgs -FailureMessage 'EasyProxy GHCR deployment failed'
    Write-Host 'EasyProxy deployment finished.'
    return
}

$env:EASY_PROXY_SERVICE_NETWORK = $networkName
if (-not [string]::IsNullOrWhiteSpace($ContainerName)) { $env:EASY_PROXY_SERVICE_CONTAINER_NAME = $ContainerName }
if (-not [string]::IsNullOrWhiteSpace($PoolPortBinding)) { $env:EASY_PROXY_SERVICE_POOL_PORT_BINDING = $PoolPortBinding }
if (-not [string]::IsNullOrWhiteSpace($ManagementPortBinding)) { $env:EASY_PROXY_SERVICE_MANAGEMENT_PORT_BINDING = $ManagementPortBinding }
if (-not [string]::IsNullOrWhiteSpace($MultiPortBinding)) { $env:EASY_PROXY_SERVICE_MULTI_PORT_BINDING = $MultiPortBinding }
if (-not [string]::IsNullOrWhiteSpace($NetworkAlias)) { $env:EASY_PROXY_SERVICE_NETWORK_ALIAS = $NetworkAlias }
$resolvedComposeProjectName = if (-not [string]::IsNullOrWhiteSpace($ComposeProjectName)) { $ComposeProjectName } else { 'easy-proxy' }
& docker network inspect $networkName *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Host "Creating docker network: $networkName" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath 'docker' -Arguments @('network', 'create', $networkName) -FailureMessage "Failed to create docker network $networkName"
}

$composeArgs = @('compose', '-p', $resolvedComposeProjectName, '-f', $composeFile, 'up', '-d')
if (-not $NoBuild) {
    $composeArgs += '--build'
}

Write-Host "Deploying EasyProxy via Docker Compose..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath 'docker' -Arguments $composeArgs -FailureMessage "EasyProxy Docker Compose deploy failed"
Write-Host 'EasyProxy deployment finished.'
