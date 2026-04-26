param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [switch]$NoBuild,
    [switch]$SkipRender
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-config.ps1')

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$serviceBase = Get-EasyProxyConfigSection -Config $config -Name 'serviceBase'
$composeFile = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $serviceBase -Name 'composeFile' -Default 'deploy/service/base/docker-compose.yaml')
$serviceOutput = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $serviceBase -Name 'renderedConfigPath' -Default 'deploy/service/base/config.yaml')

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

$composeArgs = @('compose', '-f', $composeFile, 'up', '-d')
if (-not $NoBuild) {
    $composeArgs += '--build'
}

Write-Host "Deploying EasyProxy via Docker Compose..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath 'docker' -Arguments $composeArgs -FailureMessage "EasyProxy Docker Compose deploy failed"
Write-Host 'EasyProxy deployment finished.'
