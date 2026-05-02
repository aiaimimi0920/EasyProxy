param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [ValidateSet("pages", "docker")]
    [string]$Mode = "pages",
    [string]$ProjectName = "",
    [string]$Branch = "",
    [switch]$NoInstall,
    [switch]$NoBuild,
    [switch]$SkipRender
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-config.ps1")

$config = Read-EasyProxyConfig -ConfigPath $ConfigPath
$misub = Get-EasyProxyConfigSection -Config $config -Name 'misub'
$misubRoot = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $misub -Name 'projectRoot' -Default 'upstreams/misub')
$pages = Get-EasyProxyConfigSection -Config $misub -Name 'pages'
$docker = Get-EasyProxyConfigSection -Config $misub -Name 'docker'

if ($Mode -eq "pages") {
    Assert-EasyProxyCommand -Name "npm" -Hint "Install Node.js first."
    Assert-EasyProxyCommand -Name "npx" -Hint "Install Node.js first."

    if (-not $NoInstall) {
        Write-Host "Installing MiSub dependencies..." -ForegroundColor Cyan
        Invoke-EasyProxyExternalCommand -FilePath "npm" -Arguments @("install") -WorkingDirectory $misubRoot -FailureMessage "MiSub npm install failed"
    }

    if (-not $NoBuild) {
        Write-Host "Building MiSub..." -ForegroundColor Cyan
        Invoke-EasyProxyExternalCommand -FilePath "npm" -Arguments @("run", "build") -WorkingDirectory $misubRoot -FailureMessage "MiSub build failed"
    }

    if ([string]::IsNullOrWhiteSpace($ProjectName)) {
        $ProjectName = [string](Get-EasyProxyConfigValue -Object $pages -Name 'projectName' -Default '')
    }
    if ([string]::IsNullOrWhiteSpace($Branch)) {
        $Branch = [string](Get-EasyProxyConfigValue -Object $pages -Name 'branch' -Default 'main')
    }
    if ([string]::IsNullOrWhiteSpace($ProjectName)) {
        $wranglerConfig = Join-Path $misubRoot "wrangler.jsonc"
        Ensure-EasyProxyPathExists -Path $wranglerConfig -Message "Missing MiSub wrangler config: $wranglerConfig"
        $ProjectName = Get-EasyProxyJsoncStringProperty -Path $wranglerConfig -Name "name"
    }
    if ([string]::IsNullOrWhiteSpace($ProjectName)) {
        $ProjectName = "misub-git"
    }

    Write-Host "Deploying MiSub to Cloudflare Pages project: $ProjectName" -ForegroundColor Cyan
    Invoke-EasyProxyExternalCommand `
        -FilePath "npx" `
        -Arguments @("wrangler", "pages", "deploy", "dist", "--project-name", $ProjectName, "--branch", $Branch, "--commit-dirty=true") `
        -WorkingDirectory $misubRoot `
        -FailureMessage "MiSub Cloudflare Pages deploy failed"

    Write-Host "MiSub Pages deploy finished."
    return
}

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$composeFile = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $docker -Name 'composeFile' -Default 'upstreams/misub/docker-compose.yml')
$envFile = Resolve-EasyProxyPath -Path (Get-EasyProxyConfigValue -Object $docker -Name 'envOutput' -Default 'upstreams/misub/.env') -AllowMissing
Ensure-EasyProxyPathExists -Path $composeFile -Message "Missing MiSub docker compose file: $composeFile"
if (-not $SkipRender) {
    $render = Join-Path $PSScriptRoot 'render-derived-configs.ps1'
    Invoke-EasyProxyExternalCommand -FilePath 'powershell' -Arguments @(
        '-ExecutionPolicy', 'Bypass',
        '-File', $render,
        '-ConfigPath', (Resolve-EasyProxyPath -Path $ConfigPath),
        '-MiSub',
        '-MiSubEnvOutput', $envFile
    ) -FailureMessage "Failed to render MiSub .env from root config"
}
Ensure-EasyProxyPathExists -Path $envFile -Message "Missing MiSub .env. Render it from config.yaml or copy .env.example to .env first."

$args = @("compose", "-f", $composeFile, "up", "-d")
if (-not $NoBuild) {
    $args += "--build"
}

Write-Host "Deploying MiSub via Docker Compose..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -WorkingDirectory $misubRoot -FailureMessage "MiSub Docker deploy failed"
Write-Host "MiSub Docker deploy finished."
