param(
    [ValidateSet("pages", "docker")]
    [string]$Mode = "pages",
    [string]$ProjectName = "",
    [string]$Branch = "main",
    [switch]$NoInstall,
    [switch]$NoBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")

$misubRoot = Resolve-EasyProxyPath -Path "upstreams/misub"

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

$composeFile = Join-Path $misubRoot "docker-compose.yml"
$envFile = Join-Path $misubRoot ".env"
Ensure-EasyProxyPathExists -Path $composeFile -Message "Missing MiSub docker compose file: $composeFile"
Ensure-EasyProxyPathExists -Path $envFile -Message "Missing MiSub .env. Copy .env.example to .env and fill the runtime secrets first."

$args = @("compose", "-f", $composeFile, "up", "-d")
if (-not $NoBuild) {
    $args += "--build"
}

Write-Host "Deploying MiSub via Docker Compose..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -WorkingDirectory $misubRoot -FailureMessage "MiSub Docker deploy failed"
Write-Host "MiSub Docker deploy finished."
