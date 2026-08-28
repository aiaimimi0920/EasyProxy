[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [string]$Version = '',
    [string]$PackagePath = '',
    [string]$BaseUrl = '',
    [switch]$ReplaceConfig,
    [switch]$Rollback,
    [string]$BackupPath = '',
    [string]$InstallRoot = "$env:ProgramFiles\EasyProxy",
    [string]$DataRoot = "$env:ProgramData\EasyProxy"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$serviceName = 'EasyProxy'
$releaseRoot = Join-Path $InstallRoot 'releases'
$configPath = Join-Path $DataRoot 'config.yaml'
$statePath = Join-Path $DataRoot 'data'
$backupRoot = Join-Path $DataRoot 'backups'
$serviceExistedBefore = $null -ne (Get-Service $serviceName -ErrorAction SilentlyContinue)

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this installer from an elevated PowerShell session.'
    }
}

function New-RuntimeBackup {
    param([string]$PreviousVersion)
    $target = if ([string]::IsNullOrWhiteSpace($Version)) { 'rollback' } else { $Version }
    $path = Join-Path $backupRoot "before-$target-$([DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ'))"
    $null = New-Item -ItemType Directory -Force -Path $path
    $hadConfig = Test-Path -LiteralPath $configPath -PathType Leaf
    $hadData = Test-Path -LiteralPath $statePath -PathType Container
    if ($hadConfig) { Copy-Item $configPath (Join-Path $path 'config.yaml') }
    if ($hadData) { Copy-Item $statePath (Join-Path $path 'data') -Recurse }
    @{ schemaVersion = 1; previousVersion = $PreviousVersion; hadConfig = $hadConfig; hadData = $hadData } | ConvertTo-Json |
        Set-Content -LiteralPath (Join-Path $path 'metadata.json') -Encoding utf8
    return $path
}

function Set-ServiceBinary {
    param([Parameter(Mandatory = $true)][string]$ReleaseDirectory)
    $binary = Join-Path $ReleaseDirectory 'bin\easy-proxy.exe'
    $binaryPath = "`"$binary`" --config `"$configPath`""
    & sc.exe query $serviceName *> $null
    if ($LASTEXITCODE -eq 0) {
        & sc.exe config $serviceName binPath= $binaryPath start= auto | Out-Null
    } else {
        & sc.exe create $serviceName binPath= $binaryPath start= auto DisplayName= 'EasyProxy' | Out-Null
    }
    if ($LASTEXITCODE -ne 0) { throw 'Failed to configure the EasyProxy Windows Service.' }
}

function Restore-RuntimeBackup {
    param([Parameter(Mandatory = $true)][string]$Path)
    $backup = [IO.Path]::GetFullPath($Path)
    $allowed = [IO.Path]::GetFullPath($backupRoot).TrimEnd('\') + '\'
    if (-not $backup.StartsWith($allowed, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Backup is outside $backupRoot"
    }
    $metadataPath = Join-Path $backup 'metadata.json'
    if (-not (Test-Path $metadataPath)) { throw "Invalid backup: $backup" }
    $metadata = Get-Content $metadataPath -Raw | ConvertFrom-Json
    $release = Join-Path $releaseRoot ([string]$metadata.previousVersion)
    if (-not [string]::IsNullOrWhiteSpace([string]$metadata.previousVersion) -and -not (Test-Path (Join-Path $release 'bin\easy-proxy.exe'))) {
        throw "Missing previous release: $release"
    }
    Stop-Service $serviceName -Force -ErrorAction SilentlyContinue
    if ([bool]$metadata.hadConfig) { Copy-Item (Join-Path $backup 'config.yaml') $configPath -Force } else { Remove-Item $configPath -Force -ErrorAction SilentlyContinue }
    Remove-Item $statePath -Recurse -Force -ErrorAction SilentlyContinue
    if ([bool]$metadata.hadData) { Copy-Item (Join-Path $backup 'data') $statePath -Recurse } else { $null = New-Item -ItemType Directory -Force -Path $statePath }
    if (-not [string]::IsNullOrWhiteSpace([string]$metadata.previousVersion)) {
        Set-ServiceBinary -ReleaseDirectory $release
        Set-Content -LiteralPath (Join-Path $InstallRoot 'current.txt') -Value ([string]$metadata.previousVersion) -Encoding ascii
        Start-Service $serviceName
    }
}

Assert-Administrator
$null = New-Item -ItemType Directory -Force -Path $releaseRoot, $DataRoot, $statePath, $backupRoot
if ($Rollback) {
    if ([string]::IsNullOrWhiteSpace($BackupPath)) {
        $BackupPath = (Get-ChildItem $backupRoot -Directory | Sort-Object Name -Descending | Select-Object -First 1).FullName
    }
    if ([string]::IsNullOrWhiteSpace($BackupPath)) { throw "No backup exists under $backupRoot" }
    $currentFile = Join-Path $InstallRoot 'current.txt'
    $currentVersion = if (Test-Path $currentFile) { (Get-Content $currentFile -Raw).Trim() } else { '' }
    Stop-Service $serviceName -Force -ErrorAction SilentlyContinue
    $safetyBackup = New-RuntimeBackup -PreviousVersion $currentVersion
    Restore-RuntimeBackup -Path $BackupPath
    Write-Host "EasyProxy rollback completed from $BackupPath" -ForegroundColor Green
    Write-Host "Pre-rollback safety backup: $safetyBackup"
    exit 0
}
if ([string]::IsNullOrWhiteSpace($Version)) { throw '-Version is required for install or update.' }

$temporary = Join-Path ([IO.Path]::GetTempPath()) "easyproxy-$([guid]::NewGuid().ToString('N'))"
$null = New-Item -ItemType Directory -Path $temporary
$backup = ''
$release = ''
try {
    if ([string]::IsNullOrWhiteSpace($PackagePath)) {
        if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
            $BaseUrl = "https://github.com/aiaimimi0920/EasyProxy/releases/download/$Version"
        }
        $PackagePath = Join-Path $temporary 'easyproxy-windows-amd64.zip'
        Invoke-WebRequest "$BaseUrl/easyproxy-windows-amd64.zip" -OutFile $PackagePath
        $sums = Join-Path $temporary 'SHA256SUMS'
        Invoke-WebRequest "$BaseUrl/SHA256SUMS" -OutFile $sums
        $line = Get-Content $sums | Where-Object { $_ -match '\s+easyproxy-windows-amd64\.zip$' } | Select-Object -First 1
        if ($null -eq $line) { throw 'Package checksum is missing from SHA256SUMS.' }
        $expected = ($line -split '\s+')[0].ToLowerInvariant()
        $actual = (Get-FileHash $PackagePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected) { throw 'EasyProxy package checksum mismatch.' }
    }
    $release = Join-Path $releaseRoot $Version
    if (Test-Path $release) { throw "Release already installed: $Version" }
    Expand-Archive $PackagePath $release
    if (-not (Test-Path (Join-Path $release 'bin\easy-proxy.exe'))) { throw 'Package lacks bin\easy-proxy.exe.' }

    $currentFile = Join-Path $InstallRoot 'current.txt'
    $previousVersion = if (Test-Path $currentFile) { (Get-Content $currentFile -Raw).Trim() } else { '' }
    Stop-Service $serviceName -Force -ErrorAction SilentlyContinue
    $backup = New-RuntimeBackup -PreviousVersion $previousVersion
    $example = Join-Path $release 'config.example.yaml'
    if (-not (Test-Path $configPath)) {
        Copy-Item $example $configPath
    } elseif ($ReplaceConfig) {
        Copy-Item $configPath "$configPath.previous" -Force
        Copy-Item $example $configPath -Force
    }
    Set-ServiceBinary -ReleaseDirectory $release
    if (-not [string]::IsNullOrWhiteSpace($previousVersion)) {
        Set-Content (Join-Path $InstallRoot 'previous.txt') $previousVersion -Encoding ascii
    }
    Set-Content $currentFile $Version -Encoding ascii
    foreach ($port in 22323, 29888) {
        $rule = "EasyProxy TCP $port"
        if (-not (Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -DisplayName $rule -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port | Out-Null
        }
    }
    Start-Service $serviceName
    (Get-Service $serviceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
    foreach ($port in 22323, 29888) {
        $ready = $false
        for ($attempt = 0; $attempt -lt 30; $attempt++) {
            if (Test-NetConnection -ComputerName 127.0.0.1 -Port $port -InformationLevel Quiet -WarningAction SilentlyContinue) {
                $ready = $true
                break
            }
            Start-Sleep -Seconds 1
        }
        if (-not $ready) { throw "EasyProxy port $port did not become ready." }
    }
    Write-Host "EasyProxy $Version installed; rollback backup: $backup" -ForegroundColor Green
}
catch {
    $failure = $_
    if (-not [string]::IsNullOrWhiteSpace($backup)) {
        Write-Warning "Install failed; restoring runtime backup $backup"
        try { Restore-RuntimeBackup -Path $backup } catch { Write-Warning "Automatic runtime restore failed: $_" }
    }
    if (-not $serviceExistedBefore) {
        Stop-Service $serviceName -Force -ErrorAction SilentlyContinue
        & sc.exe delete $serviceName | Out-Null
    }
    if (-not [string]::IsNullOrWhiteSpace($release)) {
        Remove-Item $release -Recurse -Force -ErrorAction SilentlyContinue
    }
    throw $failure
}
finally {
    Remove-Item $temporary -Recurse -Force -ErrorAction SilentlyContinue
}
