Set-StrictMode -Version Latest

function Assert-EasyProxyChildPath {
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string]$Path
    )

    $rootFull = [System.IO.Path]::GetFullPath($Root).TrimEnd('\', '/')
    $pathFull = [System.IO.Path]::GetFullPath($Path)
    $prefix = $rootFull + [System.IO.Path]::DirectorySeparatorChar
    if (-not $pathFull.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Path escapes EasyProxy runtime root: $pathFull"
    }
    return $pathFull
}

function Copy-EasyProxyFileAtomic {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    $destinationFull = [System.IO.Path]::GetFullPath($Destination)
    $parent = Split-Path -Parent $destinationFull
    $null = New-Item -ItemType Directory -Force -Path $parent
    $temporary = "$destinationFull.tmp-$([guid]::NewGuid().ToString('N'))"
    try {
        Copy-Item -LiteralPath $Source -Destination $temporary -Force
        Move-Item -LiteralPath $temporary -Destination $destinationFull -Force
    }
    finally {
        Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
    }
}

function New-EasyProxyRuntimeBackup {
    param(
        [Parameter(Mandatory = $true)][string]$RuntimeRoot,
        [string]$PreviousImage = ''
    )

    $root = [System.IO.Path]::GetFullPath($RuntimeRoot)
    $backupRoot = Join-Path $root 'backups'
    $null = New-Item -ItemType Directory -Force -Path $backupRoot
    $backupName = "runtime-$([DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ'))-$([guid]::NewGuid().ToString('N').Substring(0, 8))"
    $backupPath = Join-Path $backupRoot $backupName
    $null = New-Item -ItemType Directory -Path $backupPath

    $configPath = Join-Path $root 'config.yaml'
    $dataPath = Join-Path $root 'data'
    $envPath = Join-Path $root '.env'
    $hadConfig = Test-Path -LiteralPath $configPath -PathType Leaf
    $hadData = Test-Path -LiteralPath $dataPath -PathType Container
    $hadEnv = Test-Path -LiteralPath $envPath -PathType Leaf

    if ($hadConfig) { Copy-Item -LiteralPath $configPath -Destination (Join-Path $backupPath 'config.yaml') }
    if ($hadData) { Copy-Item -LiteralPath $dataPath -Destination (Join-Path $backupPath 'data') -Recurse }
    if ($hadEnv) { Copy-Item -LiteralPath $envPath -Destination (Join-Path $backupPath '.env') }

    $metadata = [ordered]@{
        schemaVersion = 1
        createdAt = [DateTime]::UtcNow.ToString('o')
        previousImage = $PreviousImage
        hadConfig = $hadConfig
        hadData = $hadData
        hadEnv = $hadEnv
    }
    $metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $backupPath 'metadata.json') -Encoding utf8
    return $backupPath
}

function Restore-EasyProxyRuntimeBackup {
    param(
        [Parameter(Mandatory = $true)][string]$RuntimeRoot,
        [Parameter(Mandatory = $true)][string]$BackupPath
    )

    $root = [System.IO.Path]::GetFullPath($RuntimeRoot)
    $backupRoot = Join-Path $root 'backups'
    $backup = Assert-EasyProxyChildPath -Root $backupRoot -Path $BackupPath
    $metadataPath = Join-Path $backup 'metadata.json'
    if (-not (Test-Path -LiteralPath $metadataPath -PathType Leaf)) {
        throw "Invalid EasyProxy runtime backup: $backup"
    }
    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json

    $configPath = Assert-EasyProxyChildPath -Root $root -Path (Join-Path $root 'config.yaml')
    $dataPath = Assert-EasyProxyChildPath -Root $root -Path (Join-Path $root 'data')
    $envPath = Assert-EasyProxyChildPath -Root $root -Path (Join-Path $root '.env')

    if ([bool]$metadata.hadConfig) {
        Copy-EasyProxyFileAtomic -Source (Join-Path $backup 'config.yaml') -Destination $configPath
    } else {
        Remove-Item -LiteralPath $configPath -Force -ErrorAction SilentlyContinue
    }

    Remove-Item -LiteralPath $dataPath -Recurse -Force -ErrorAction SilentlyContinue
    if ([bool]$metadata.hadData) {
        Copy-Item -LiteralPath (Join-Path $backup 'data') -Destination $dataPath -Recurse
    } else {
        $null = New-Item -ItemType Directory -Force -Path $dataPath
    }

    if ([bool]$metadata.hadEnv) {
        Copy-EasyProxyFileAtomic -Source (Join-Path $backup '.env') -Destination $envPath
    } else {
        Remove-Item -LiteralPath $envPath -Force -ErrorAction SilentlyContinue
    }
    return $metadata
}
