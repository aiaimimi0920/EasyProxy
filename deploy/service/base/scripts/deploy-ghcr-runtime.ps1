[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [Parameter(Mandatory = $true)]
    [string]$ConfigPath,

    [Parameter(Mandatory = $true)]
    [string]$Image,

    [string]$RuntimeRoot = 'C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\deploy\service\base',

    [string]$NetworkName = 'EasyAiMi',

    [string]$ComposeSourcePath = '',

    [string]$ContainerName = '',

    [string]$PoolPortBinding = '',

    [string]$ManagementPortBinding = '',

    [string]$MultiPortBinding = '',

    [string]$NetworkAlias = '',

    [string]$ComposeProjectName = '',

    [switch]$SkipPull,

    [switch]$ReplaceConfig
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'runtime-lifecycle.ps1')

function Resolve-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    return $item.FullName
}

function Sync-ItemIfNeeded {
    param(
        [Parameter(Mandatory = $true)][string]$SourcePath,
        [Parameter(Mandatory = $true)][string]$DestinationPath
    )

    $sourceResolved = [System.IO.Path]::GetFullPath($SourcePath)
    $destinationResolved = [System.IO.Path]::GetFullPath($DestinationPath)
    if ([string]::Equals($sourceResolved, $destinationResolved, [System.StringComparison]::OrdinalIgnoreCase)) {
        return
    }

    Copy-Item -LiteralPath $sourceResolved -Destination $destinationResolved -Force
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
$resolvedContainerName = if ([string]::IsNullOrWhiteSpace($ContainerName)) { 'easy-proxy' } else { $ContainerName }
$resolvedPoolPortBinding = if ([string]::IsNullOrWhiteSpace($PoolPortBinding)) { '22323:22323' } else { $PoolPortBinding }
$resolvedManagementPortBinding = if ([string]::IsNullOrWhiteSpace($ManagementPortBinding)) { '29888:29888' } else { $ManagementPortBinding }
$resolvedMultiPortBinding = if ([string]::IsNullOrWhiteSpace($MultiPortBinding)) { '25000-25500:25000-25500' } else { $MultiPortBinding }
$resolvedNetworkAlias = if ([string]::IsNullOrWhiteSpace($NetworkAlias)) { 'easy-proxy' } else { $NetworkAlias }
$resolvedComposeProjectName = if ([string]::IsNullOrWhiteSpace($ComposeProjectName)) { 'easy-proxy' } else { $ComposeProjectName }

$existingContainerId = (& docker ps -aq --filter "name=^$resolvedContainerName$" 2>$null | Out-String).Trim()
$previousImage = ''
if (-not [string]::IsNullOrWhiteSpace($existingContainerId)) {
    $previousImage = (& docker inspect --format '{{.Config.Image}}' $resolvedContainerName 2>$null | Out-String).Trim()
}
$backupPath = ''

if ($PSCmdlet.ShouldProcess($resolvedRuntimeRoot, "Prepare EasyProxy GHCR runtime root")) {
    $null = New-Item -ItemType Directory -Force -Path $resolvedRuntimeRoot
    $null = New-Item -ItemType Directory -Force -Path $runtimeDataPath

    if (-not [string]::IsNullOrWhiteSpace($existingContainerId)) {
        Invoke-CheckedCommand -FilePath 'docker' -Arguments @('stop', $resolvedContainerName) -FailureMessage "Failed to stop $resolvedContainerName before backup"
    }
    if ((Test-Path -LiteralPath $runtimeConfigPath) -or (Test-Path -LiteralPath $runtimeDataPath) -or (Test-Path -LiteralPath $runtimeEnvPath)) {
        $backupPath = New-EasyProxyRuntimeBackup -RuntimeRoot $resolvedRuntimeRoot -PreviousImage $previousImage
    }

    Sync-ItemIfNeeded -SourcePath $resolvedComposeSourcePath -DestinationPath $runtimeComposePath
    if (-not (Test-Path -LiteralPath $runtimeConfigPath)) {
        Copy-EasyProxyFileAtomic -Source $resolvedConfigPath -Destination $runtimeConfigPath
    } elseif ($ReplaceConfig) {
        Copy-Item -LiteralPath $runtimeConfigPath -Destination "$runtimeConfigPath.previous" -Force
        Copy-EasyProxyFileAtomic -Source $resolvedConfigPath -Destination $runtimeConfigPath
    } elseif (-not [string]::Equals($resolvedConfigPath, [System.IO.Path]::GetFullPath($runtimeConfigPath), [System.StringComparison]::OrdinalIgnoreCase)) {
        Write-Host "Preserving existing runtime config. Use -ReplaceConfig to replace it." -ForegroundColor Yellow
    }

    $envLines = @(
        "EASY_PROXY_SERVICE_IMAGE=$Image"
        "EASY_PROXY_SERVICE_NETWORK=$NetworkName"
        "EASY_PROXY_SERVICE_CONTAINER_NAME=$resolvedContainerName"
        "EASY_PROXY_SERVICE_POOL_PORT_BINDING=$resolvedPoolPortBinding"
        "EASY_PROXY_SERVICE_MANAGEMENT_PORT_BINDING=$resolvedManagementPortBinding"
        "EASY_PROXY_SERVICE_MULTI_PORT_BINDING=$resolvedMultiPortBinding"
        "EASY_PROXY_SERVICE_NETWORK_ALIAS=$resolvedNetworkAlias"
        "EASY_PROXY_SERVICE_COMPOSE_PROJECT_NAME=$resolvedComposeProjectName"
    )
    $envTemporary = "$runtimeEnvPath.tmp-$([guid]::NewGuid().ToString('N'))"
    [System.IO.File]::WriteAllLines($envTemporary, $envLines, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $envTemporary -Destination $runtimeEnvPath -Force
}

& docker network inspect $NetworkName *> $null
if ($LASTEXITCODE -ne 0) {
    if ($PSCmdlet.ShouldProcess($NetworkName, "Create Docker network for EasyProxy runtime")) {
        Invoke-CheckedCommand -FilePath 'docker' -Arguments @('network', 'create', $NetworkName) -FailureMessage "Failed to create Docker network $NetworkName"
    }
}

try {
    if (-not $SkipPull) {
        if ($PSCmdlet.ShouldProcess($Image, "Pull EasyProxy GHCR image")) {
            Invoke-CheckedCommand -FilePath 'docker' -Arguments @('pull', $Image) -FailureMessage "Failed to pull $Image"
        }
    }

    if (-not [string]::IsNullOrWhiteSpace($existingContainerId)) {
        if ($PSCmdlet.ShouldProcess($resolvedContainerName, 'Remove existing EasyProxy container before compose redeploy')) {
            Invoke-CheckedCommand -FilePath 'docker' -Arguments @('rm', '-f', $resolvedContainerName) -FailureMessage "Failed to remove existing $resolvedContainerName container"
        }
    }

    if ($PSCmdlet.ShouldProcess($runtimeComposePath, "Deploy EasyProxy service container from GHCR")) {
        Invoke-CheckedCommand -FilePath 'docker' -Arguments @(
            'compose',
            '-p', $resolvedComposeProjectName,
            '--env-file', $runtimeEnvPath,
            '-f', $runtimeComposePath,
            'up', '-d', '--remove-orphans'
        ) -FailureMessage 'Docker Compose deployment failed'
    }

    $deployedImage = (& docker inspect --format '{{.Config.Image}}' $resolvedContainerName 2>$null)
    if ($LASTEXITCODE -ne 0) { throw "Failed to inspect $resolvedContainerName after deployment" }
    $deployedImage = ($deployedImage | Out-String).Trim()
    if ($deployedImage -ne $Image -and -not $deployedImage.StartsWith("$Image@")) {
        throw "Deployed container image mismatch. Expected $Image, got $deployedImage"
    }
}
catch {
    if (-not [string]::IsNullOrWhiteSpace($backupPath)) {
        Write-Warning "Deployment failed; restoring runtime backup $backupPath"
        & docker rm -f $resolvedContainerName 2>$null | Out-Null
        $metadata = Restore-EasyProxyRuntimeBackup -RuntimeRoot $resolvedRuntimeRoot -BackupPath $backupPath
        $restoreArgs = @('compose', '-p', $resolvedComposeProjectName)
        if (Test-Path -LiteralPath $runtimeEnvPath -PathType Leaf) {
            $restoreArgs += @('--env-file', $runtimeEnvPath)
        } elseif (-not [string]::IsNullOrWhiteSpace([string]$metadata.previousImage)) {
            $env:EASY_PROXY_SERVICE_IMAGE = [string]$metadata.previousImage
        } else {
            Write-Warning 'Runtime files were restored, but no previous image metadata exists; service restart requires operator action.'
            throw
        }
        $restoreArgs += @('-f', $runtimeComposePath, 'up', '-d', '--remove-orphans')
        & docker @restoreArgs | Out-Null
        if ($LASTEXITCODE -ne 0) { Write-Warning 'Runtime files were restored, but the previous container failed to restart.' }
    }
    throw
}

Write-Host "EasyProxy GHCR runtime deployed successfully." -ForegroundColor Green
Write-Host "Runtime root: $resolvedRuntimeRoot"
Write-Host "Image: $Image"
Write-Host "Container name: $resolvedContainerName"
Write-Host "Compose project: $resolvedComposeProjectName"
if (-not [string]::IsNullOrWhiteSpace($backupPath)) { Write-Host "Rollback backup: $backupPath" }
