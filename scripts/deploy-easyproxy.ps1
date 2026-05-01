param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [switch]$NoBuild,
    [switch]$SkipRender,
    [switch]$FromGhcr,
    [string]$Image = '',
    [string]$ReleaseTag = '',
    [string]$GhcrOwner = '',
    [switch]$SkipPull
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-config.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-ghcr.ps1')

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$serviceBase = Get-EasyProxyConfigSection -Config $config -Name 'serviceBase'
$composeFile = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $serviceBase -Name 'composeFile' -Default 'deploy/service/base/docker-compose.yaml')
$serviceOutput = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $serviceBase -Name 'renderedConfigPath' -Default 'deploy/service/base/config.yaml')
$networkName = [string](Get-EasyProxyConfigValue -Object $serviceBase -Name 'networkName' -Default 'EasyAiMi')
$useGhcrDeploy = $FromGhcr -or -not [string]::IsNullOrWhiteSpace($Image) -or -not [string]::IsNullOrWhiteSpace($ReleaseTag)

Ensure-EasyProxyPathExists -Path $composeFile -Message "Missing EasyProxy docker compose file: $composeFile"

if (-not $SkipRender) {
    $render = Join-Path $PSScriptRoot 'render-derived-configs.ps1'
    Invoke-EasyProxyExternalCommand -FilePath 'powershell' -Arguments @(
        '-ExecutionPolicy', 'Bypass',
        '-File', $render,
        '-ConfigPath', (Resolve-EasyProxyPath -Path $ConfigPath),
        '-ServiceBase',
        '-ServiceOutput', $serviceOutput
    ) -FailureMessage "Failed to render EasyProxy runtime config from root config"
}

Ensure-EasyProxyPathExists -Path $serviceOutput -Message "Missing rendered EasyProxy runtime config: $serviceOutput"

if ($useGhcrDeploy) {
    if ([string]::IsNullOrWhiteSpace($Image)) {
        if ([string]::IsNullOrWhiteSpace($ReleaseTag)) {
            throw "GHCR deployment requires -Image or -ReleaseTag."
        }

        $ghcr = Get-EasyProxyConfigSection -Config $config -Name 'ghcr'
        if ([string]::IsNullOrWhiteSpace($GhcrOwner)) {
            $GhcrOwner = [string](Get-EasyProxyConfigValue -Object $ghcr -Name 'owner' -Default '')
        }
        Assert-EasyProxyGhcrOwnerIsSafe -Owner $GhcrOwner -SourceDescription "GHCR owner"

        $serviceImageName = [string](Get-EasyProxyConfigValue -Object $ghcr -Name 'serviceImageName' -Default 'easy-proxy-monorepo-service')
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
    if ($SkipPull) {
        $ghcrArgs += '-SkipPull'
    }

    Write-Host "Deploying EasyProxy from GHCR image: $Image" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath 'powershell' -Arguments $ghcrArgs -FailureMessage 'EasyProxy GHCR deployment failed'
    Write-Host 'EasyProxy deployment finished.'
    return
}

$env:EASY_PROXY_SERVICE_NETWORK = $networkName
& docker network inspect $networkName *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Host "Creating docker network: $networkName" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand -FilePath 'docker' -Arguments @('network', 'create', $networkName) -FailureMessage "Failed to create docker network $networkName"
}

$composeArgs = @('compose', '-f', $composeFile, 'up', '-d')
if (-not $NoBuild) {
    $composeArgs += '--build'
}

Write-Host "Deploying EasyProxy via Docker Compose..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath 'docker' -Arguments $composeArgs -FailureMessage "EasyProxy Docker Compose deploy failed"
Write-Host 'EasyProxy deployment finished.'
