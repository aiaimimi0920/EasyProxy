param(
    [string]$TopologyPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [ValidateSet("pages", "docker")]
    [string]$Mode = "pages",
    [string]$ProjectName = "",
    [string]$Branch = "",
    [switch]$NoInstall,
    [switch]$NoBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib\easyproxy-common.ps1")
. (Join-Path $PSScriptRoot "lib\easyproxy-topology.ps1")

$topology = Read-EasyProxyTopology -TopologyPath $TopologyPath
if (-not [bool]$topology.components.misub) {
    throw 'Topology does not enable components.misub.'
}
$resourceNames = Get-EasyProxyResourceNames -TopologyPath $TopologyPath
$misubRoot = Resolve-EasyProxyPath -Path 'upstreams/misub'

if ($Mode -eq "pages") {
    Assert-EasyProxyCommand -Name "npm" -Hint "Install Node.js first."
    Assert-EasyProxyCommand -Name "npx" -Hint "Install Node.js first."

    if (-not $NoInstall) {
        Write-Host "Installing MiSub dependencies..." -ForegroundColor Cyan
        Invoke-EasyProxyExternalCommand -FilePath "npm" -Arguments @("ci") -WorkingDirectory $misubRoot -FailureMessage "MiSub npm ci failed"
    }

    if (-not $NoBuild) {
        Write-Host "Building MiSub..." -ForegroundColor Cyan
        Invoke-EasyProxyExternalCommand -FilePath "npm" -Arguments @("run", "build") -WorkingDirectory $misubRoot -FailureMessage "MiSub build failed"
    }

    if ([string]::IsNullOrWhiteSpace($ProjectName)) {
        $ProjectName = [string]$resourceNames.pages_project
    }
    if ([string]::IsNullOrWhiteSpace($Branch)) {
        $Branch = 'main'
    }

    $previousAccountId = [Environment]::GetEnvironmentVariable('CLOUDFLARE_ACCOUNT_ID', 'Process')
    $previousApiToken = [Environment]::GetEnvironmentVariable('CLOUDFLARE_API_TOKEN', 'Process')
    try {
        $env:CLOUDFLARE_ACCOUNT_ID = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.cloudflare.account_id_env) -Purpose 'Cloudflare account selection'
        $env:CLOUDFLARE_API_TOKEN = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.cloudflare_api_token) -Purpose 'MiSub Pages deployment'

        Write-Host "Deploying MiSub to Cloudflare Pages project: $ProjectName" -ForegroundColor Cyan
        Invoke-EasyProxyExternalCommand `
            -FilePath "npx" `
            -Arguments @("wrangler", "pages", "deploy", "dist", "--project-name", $ProjectName, "--branch", $Branch, "--commit-dirty=true") `
            -WorkingDirectory $misubRoot `
            -FailureMessage "MiSub Cloudflare Pages deploy failed"
    }
    finally {
        [Environment]::SetEnvironmentVariable('CLOUDFLARE_ACCOUNT_ID', $previousAccountId, 'Process')
        [Environment]::SetEnvironmentVariable('CLOUDFLARE_API_TOKEN', $previousApiToken, 'Process')
    }

    Write-Host "MiSub Pages deploy finished."
    return
}

Assert-EasyProxyCommand -Name "docker" -Hint "Install Docker Desktop or another Docker engine first."

$composeFile = Resolve-EasyProxyPath -Path 'upstreams/misub/docker-compose.yml'
$envFile = Resolve-EasyProxyPath -Path 'upstreams/misub/.env' -AllowMissing
Ensure-EasyProxyPathExists -Path $composeFile -Message "Missing MiSub docker compose file: $composeFile"
Ensure-EasyProxyPathExists -Path $envFile -Message "Missing MiSub .env. Copy upstreams/misub/.env.example to .env and store local Docker secrets there."

$args = @("compose", "-f", $composeFile, "up", "-d")
if (-not $NoBuild) {
    $args += "--build"
}

Write-Host "Deploying MiSub via Docker Compose..." -ForegroundColor Cyan
Invoke-EasyProxyExternalCommand -FilePath "docker" -Arguments $args -WorkingDirectory $misubRoot -FailureMessage "MiSub Docker deploy failed"
Write-Host "MiSub Docker deploy finished."
