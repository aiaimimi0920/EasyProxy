[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [Parameter(Mandatory = $true)]
    [string]$ConfigPath,

    [Parameter(Mandatory = $true)]
    [string]$Image,

    [string]$RuntimeRoot = 'C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\deploy\service\base',

    [string]$NetworkName = 'EasyAiMi',

    [string]$ComposeSourcePath = '',

    [switch]$SkipPull
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    return $item.FullName
}

function Invoke-CheckedCommand {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$FailureMessage
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FailureMessage (exit code $LASTEXITCODE)"
    }
}

if (-not (Test-Path -LiteralPath $ConfigPath)) {
    throw "Missing rendered service config: $ConfigPath"
}
if ([string]::IsNullOrWhiteSpace($ComposeSourcePath)) {
    $ComposeSourcePath = Join-Path $PSScriptRoot '..\docker-compose.yaml'
}
if (-not (Test-Path -LiteralPath $ComposeSourcePath)) {
    throw "Missing compose template: $ComposeSourcePath"
}

$resolvedConfigPath = Resolve-FullPath -Path $ConfigPath
$resolvedComposeSourcePath = Resolve-FullPath -Path $ComposeSourcePath
$resolvedRuntimeRoot = [System.IO.Path]::GetFullPath($RuntimeRoot)
$runtimeConfigPath = Join-Path $resolvedRuntimeRoot 'config.yaml'
$runtimeDataPath = Join-Path $resolvedRuntimeRoot 'data'
$runtimeComposePath = Join-Path $resolvedRuntimeRoot 'docker-compose.yaml'
$runtimeEnvPath = Join-Path $resolvedRuntimeRoot '.env'

if ($PSCmdlet.ShouldProcess($resolvedRuntimeRoot, "Prepare EasyProxy GHCR runtime root")) {
    $null = New-Item -ItemType Directory -Force -Path $resolvedRuntimeRoot
    $null = New-Item -ItemType Directory -Force -Path $runtimeDataPath

    Copy-Item -LiteralPath $resolvedComposeSourcePath -Destination $runtimeComposePath -Force
    Copy-Item -LiteralPath $resolvedConfigPath -Destination $runtimeConfigPath -Force

    @(
        "EASY_PROXY_SERVICE_IMAGE=$Image"
        "EASY_PROXY_SERVICE_NETWORK=$NetworkName"
    ) | Set-Content -LiteralPath $runtimeEnvPath -Encoding utf8
}

& docker network inspect $NetworkName *> $null
if ($LASTEXITCODE -ne 0) {
    if ($PSCmdlet.ShouldProcess($NetworkName, "Create Docker network for EasyProxy runtime")) {
        Invoke-CheckedCommand -FilePath 'docker' -Arguments @('network', 'create', $NetworkName) -FailureMessage "Failed to create Docker network $NetworkName"
    }
}

if (-not $SkipPull) {
    if ($PSCmdlet.ShouldProcess($Image, "Pull EasyProxy GHCR image")) {
        Invoke-CheckedCommand -FilePath 'docker' -Arguments @('pull', $Image) -FailureMessage "Failed to pull $Image"
    }
}

$existingContainerId = (& docker ps -aq --filter 'name=^easy-proxy-monorepo-service$' 2>$null | Out-String).Trim()
if (-not [string]::IsNullOrWhiteSpace($existingContainerId)) {
    if ($PSCmdlet.ShouldProcess('easy-proxy-monorepo-service', 'Remove existing EasyProxy container before compose redeploy')) {
        Invoke-CheckedCommand -FilePath 'docker' -Arguments @('rm', '-f', 'easy-proxy-monorepo-service') -FailureMessage 'Failed to remove existing easy-proxy-monorepo-service container'
    }
}

if ($PSCmdlet.ShouldProcess($runtimeComposePath, "Deploy EasyProxy service container from GHCR")) {
    Invoke-CheckedCommand -FilePath 'docker' -Arguments @(
        'compose',
        '--env-file', $runtimeEnvPath,
        '-f', $runtimeComposePath,
        'up', '-d', '--remove-orphans'
    ) -FailureMessage 'Docker Compose deployment failed'
}

$deployedImage = (& docker inspect --format '{{.Config.Image}}' 'easy-proxy-monorepo-service' 2>$null)
if ($LASTEXITCODE -ne 0) {
    throw 'Failed to inspect easy-proxy-monorepo-service after deployment'
}
$deployedImage = ($deployedImage | Out-String).Trim()
if ($deployedImage -ne $Image -and -not $deployedImage.StartsWith("$Image@")) {
    throw "Deployed container image mismatch. Expected $Image, got $deployedImage"
}

Write-Host "EasyProxy GHCR runtime deployed successfully." -ForegroundColor Green
Write-Host "Runtime root: $resolvedRuntimeRoot"
Write-Host "Image: $Image"
