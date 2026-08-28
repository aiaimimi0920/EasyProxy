[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [string]$RuntimeRoot = 'C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\deploy\service\base',
    [string]$BackupPath = '',
    [string]$ComposeProjectName = 'easy-proxy',
    [string]$ContainerName = 'easy-proxy'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'runtime-lifecycle.ps1')

$root = [System.IO.Path]::GetFullPath($RuntimeRoot)
$backupRoot = Join-Path $root 'backups'
if ([string]::IsNullOrWhiteSpace($BackupPath)) {
    $candidate = Get-ChildItem -LiteralPath $backupRoot -Directory -ErrorAction Stop |
        Sort-Object Name -Descending | Select-Object -First 1
    if ($null -eq $candidate) { throw "No EasyProxy runtime backup exists under $backupRoot" }
    $BackupPath = $candidate.FullName
}
$BackupPath = Assert-EasyProxyChildPath -Root $backupRoot -Path $BackupPath
$composePath = Join-Path $root 'docker-compose.yaml'
if (-not (Test-Path -LiteralPath $composePath -PathType Leaf)) {
    throw "Missing runtime compose file: $composePath"
}

if ($PSCmdlet.ShouldProcess($root, "Restore EasyProxy runtime backup $BackupPath")) {
    $currentContainerId = (& docker ps -aq --filter "name=^$ContainerName$" 2>$null | Out-String).Trim()
    $currentImage = (& docker inspect --format '{{.Config.Image}}' $ContainerName 2>$null | Out-String).Trim()
    if (-not [string]::IsNullOrWhiteSpace($currentContainerId)) {
        & docker stop $ContainerName | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Failed to stop $ContainerName before rollback backup" }
    }
    $safetyBackup = New-EasyProxyRuntimeBackup -RuntimeRoot $root -PreviousImage $currentImage
    & docker rm -f $ContainerName 2>$null | Out-Null
    $metadata = Restore-EasyProxyRuntimeBackup -RuntimeRoot $root -BackupPath $BackupPath
    $envPath = Join-Path $root '.env'
    $composeArgs = @('compose', '-p', $ComposeProjectName)
    if (Test-Path -LiteralPath $envPath -PathType Leaf) {
        $composeArgs += @('--env-file', $envPath)
    } elseif (-not [string]::IsNullOrWhiteSpace([string]$metadata.previousImage)) {
        $env:EASY_PROXY_SERVICE_IMAGE = [string]$metadata.previousImage
    } else {
        throw "Backup has neither .env nor a previous image; files were restored but the container cannot be started"
    }
    $composeArgs += @('-f', $composePath, 'up', '-d', '--remove-orphans')
    & docker @composeArgs
    if ($LASTEXITCODE -ne 0) {
        throw "Rollback deployment failed. Current-state safety backup: $safetyBackup"
    }
    Write-Host "EasyProxy runtime restored from $BackupPath" -ForegroundColor Green
    Write-Host "Pre-rollback safety backup: $safetyBackup"
}
