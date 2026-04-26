param(
    [string]$SourceRoot = "C:\Users\Public\nas_home\AI\GameEditor\ProxyService",
    [string]$TargetRoot = "C:\Users\Public\nas_home\AI\GameEditor\EasyProxy"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-RequiredPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Path does not exist: $Path"
    }

    return (Resolve-Path -LiteralPath $Path).Path
}

function Ensure-WithinTarget {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [Parameter(Mandatory = $true)]
        [string]$Target
    )

    if (-not $Path.StartsWith($Target, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to write outside target root: $Path"
    }
}

function Invoke-RoboCopy {
    param(
        [Parameter(Mandatory = $true)]
        [string]$From,
        [Parameter(Mandatory = $true)]
        [string]$To,
        [string[]]$ExcludeDirs = @(),
        [string[]]$ExcludeFiles = @()
    )

    $cmd = @($From, $To, "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/NP")

    if ($ExcludeDirs.Count -gt 0) {
        $cmd += "/XD"
        $cmd += $ExcludeDirs
    }

    if ($ExcludeFiles.Count -gt 0) {
        $cmd += "/XF"
        $cmd += $ExcludeFiles
    }

    New-Item -ItemType Directory -Force -Path $To | Out-Null
    & robocopy @cmd | Out-Host
    if ($LASTEXITCODE -gt 7) {
        throw "robocopy failed with exit code $LASTEXITCODE for $From -> $To"
    }
}

$resolvedSource = Resolve-RequiredPath -Path $SourceRoot
$resolvedTarget = Resolve-RequiredPath -Path $TargetRoot

$mappings = @(
    @{
        From = Join-Path $resolvedSource "repos\EasyProxy"
        To = Join-Path $resolvedTarget "service\base"
        ExcludeDirs = @(".git")
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "repos\MiSub"
        To = Join-Path $resolvedTarget "upstreams\misub"
        ExcludeDirs = @(".git", "node_modules", "dist", ".wrangler")
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "repos\aggregator"
        To = Join-Path $resolvedTarget "upstreams\aggregator"
        ExcludeDirs = @(".git", "__pycache__", ".pytest_cache")
        ExcludeFiles = @("cache.db", "workflow.log")
    },
    @{
        From = Join-Path $resolvedSource "repos\ech-workers"
        To = Join-Path $resolvedTarget "upstreams\ech-workers"
        ExcludeDirs = @(".git")
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "repos\ech-workers-cloudflare"
        To = Join-Path $resolvedTarget "workers\ech-workers-cloudflare"
        ExcludeDirs = @(".git", ".wrangler")
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "deploy\EasyProxy"
        To = Join-Path $resolvedTarget "deploy\service\base"
        ExcludeDirs = @("data")
        ExcludeFiles = @("config.yaml")
    },
    @{
        From = Join-Path $resolvedSource "deploy\MiSub"
        To = Join-Path $resolvedTarget "deploy\upstreams\misub"
        ExcludeDirs = @()
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "deploy\aggregator"
        To = Join-Path $resolvedTarget "deploy\upstreams\aggregator"
        ExcludeDirs = @()
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "deploy\ech-workers"
        To = Join-Path $resolvedTarget "deploy\upstreams\ech-workers"
        ExcludeDirs = @()
        ExcludeFiles = @()
    },
    @{
        From = Join-Path $resolvedSource "deploy\ech-workers-cloudflare"
        To = Join-Path $resolvedTarget "deploy\workers\ech-workers-cloudflare"
        ExcludeDirs = @()
        ExcludeFiles = @()
    }
)

foreach ($mapping in $mappings) {
    $from = Resolve-RequiredPath -Path $mapping.From
    $to = $mapping.To
    Ensure-WithinTarget -Path $to -Target $resolvedTarget
    Write-Host "Syncing $from -> $to"
    Invoke-RoboCopy -From $from -To $to -ExcludeDirs $mapping.ExcludeDirs -ExcludeFiles $mapping.ExcludeFiles
}

Write-Host "ProxyService copy sync completed."
