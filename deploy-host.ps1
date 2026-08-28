param(
    [ValidateSet(
        'easyproxy',
        'easyproxy-ghcr',
        'misub-pages',
        'misub-docker',
        'aggregator',
        'ech-workers-cloudflare',
        'build-easyproxy-image',
        'build-ech-workers-image',
        'publish-service-base-config',
        'publish-easyproxy-image',
        'publish-ech-workers-image',
        'publish-core-images'
    )]
    [string]$Project = 'easyproxy',
    [string]$TopologyPath = 'topology.yaml',
    [string]$RuntimeConfigPath = 'easyproxy-runtime.yaml',
    [string]$ImportCode = '',
    [string]$BootstrapFile = '',
    [switch]$NoBuild,
    [switch]$NoInstall,
    [switch]$DryRun,
    [switch]$SkipSecretSync,
    [switch]$SkipWorkflowTrigger,
    [switch]$NoCache,
    [switch]$Push,
    [string]$ReleaseTag = '',
    [string]$GhcrOwner = '',
    [string]$GhcrUsername = '',
    [switch]$LoadOnly,
    [string]$Image = '',
    [switch]$SkipPull,
    [string]$ContainerName = 'easy-proxy',
    [string]$PoolPortBinding = '',
    [string]$ManagementPortBinding = '',
    [string]$MultiPortBinding = '',
    [string]$NetworkAlias = 'easy-proxy',
    [string]$ComposeProjectName = 'easy-proxy',
    [string]$RepoOwner = 'aiaimimi0920',
    [string]$RepoName = 'EasyProxy',
    [string]$RepoRef = 'main',
    [string]$RepoCacheRoot = '',
    [switch]$ForceRefreshRepo,
    [switch]$ResolveRepoOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-AbsolutePath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$BaseDir
    )

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $BaseDir $Path))
}

function Test-RepositoryLayout {
    param([Parameter(Mandatory = $true)][string]$Root)

    return (
        (Test-Path -LiteralPath (Join-Path $Root 'topology.example.yaml')) -and
        (Test-Path -LiteralPath (Join-Path $Root 'scripts\deploy-subproject.ps1')) -and
        (Test-Path -LiteralPath (Join-Path $Root 'tools\easyproxyctl\go.mod'))
    )
}

$launcherRoot = Split-Path -Parent $PSCommandPath
$repoRoot = $launcherRoot
$source = 'local'
if (-not (Test-RepositoryLayout -Root $repoRoot)) {
    if ($null -eq (Get-Command git -ErrorAction SilentlyContinue)) {
        throw 'Git is required to clone EasyProxy with its recursive submodules.'
    }
    $cacheRoot = if ([string]::IsNullOrWhiteSpace($RepoCacheRoot)) {
        Join-Path $launcherRoot '.repo-cache'
    } else {
        Resolve-AbsolutePath -Path $RepoCacheRoot -BaseDir $launcherRoot
    }
    $repoRoot = Join-Path $cacheRoot "$RepoName-$RepoRef"
    if ($ForceRefreshRepo -and (Test-Path -LiteralPath $repoRoot)) {
        $resolvedCache = [System.IO.Path]::GetFullPath($cacheRoot)
        $resolvedRepo = [System.IO.Path]::GetFullPath($repoRoot)
        if (-not $resolvedRepo.StartsWith($resolvedCache + [System.IO.Path]::DirectorySeparatorChar)) {
            throw "Refusing to refresh repository outside cache root: $resolvedRepo"
        }
        Remove-Item -LiteralPath $resolvedRepo -Recurse -Force
    }
    if (-not (Test-RepositoryLayout -Root $repoRoot)) {
        New-Item -ItemType Directory -Force -Path $cacheRoot | Out-Null
        $url = "https://github.com/$RepoOwner/$RepoName.git"
        & git clone --recurse-submodules --depth 1 --branch $RepoRef $url $repoRoot
        if ($LASTEXITCODE -ne 0) {
            throw "Recursive EasyProxy clone failed with exit code $LASTEXITCODE"
        }
    }
    $source = 'recursive-clone'
}

if (-not (Test-RepositoryLayout -Root $repoRoot)) {
    throw "EasyProxy repository layout is incomplete: $repoRoot"
}

if ($ResolveRepoOnly) {
    [pscustomobject]@{ RepoRoot = $repoRoot; Source = $source } | Format-List
    return
}

$resolvedTopologyPath = Resolve-AbsolutePath -Path $TopologyPath -BaseDir $launcherRoot
if (-not (Test-Path -LiteralPath $resolvedTopologyPath)) {
    $topologyParent = Split-Path -Parent $resolvedTopologyPath
    if (-not [string]::IsNullOrWhiteSpace($topologyParent)) {
        New-Item -ItemType Directory -Force -Path $topologyParent | Out-Null
    }
    Copy-Item -LiteralPath (Join-Path $repoRoot 'topology.example.yaml') -Destination $resolvedTopologyPath
    Write-Host "[deploy-host] created topology: $resolvedTopologyPath" -ForegroundColor Yellow
}
$resolvedRuntimeConfigPath = Resolve-AbsolutePath -Path $RuntimeConfigPath -BaseDir $launcherRoot

$arguments = @(
    '-ExecutionPolicy', 'Bypass',
    '-File', (Join-Path $repoRoot 'scripts\deploy-subproject.ps1'),
    '-Project', $Project,
    '-TopologyPath', $resolvedTopologyPath,
    '-RuntimeConfigPath', $resolvedRuntimeConfigPath
)
foreach ($pair in @(
    @('ImportCode', $ImportCode),
    @('BootstrapFile', $BootstrapFile),
    @('ReleaseTag', $ReleaseTag),
    @('GhcrOwner', $GhcrOwner),
    @('GhcrUsername', $GhcrUsername),
    @('Image', $Image),
    @('ContainerName', $ContainerName),
    @('PoolPortBinding', $PoolPortBinding),
    @('ManagementPortBinding', $ManagementPortBinding),
    @('MultiPortBinding', $MultiPortBinding),
    @('NetworkAlias', $NetworkAlias),
    @('ComposeProjectName', $ComposeProjectName)
)) {
    if (-not [string]::IsNullOrWhiteSpace([string]$pair[1])) {
        $arguments += @("-$($pair[0])", [string]$pair[1])
    }
}
foreach ($switchName in @(
    'NoBuild', 'NoInstall', 'DryRun', 'SkipSecretSync', 'SkipWorkflowTrigger',
    'NoCache', 'Push', 'LoadOnly', 'SkipPull'
)) {
    if ([bool](Get-Variable -Name $switchName -ValueOnly)) {
        $arguments += "-$switchName"
    }
}

& powershell @arguments
if ($LASTEXITCODE -ne 0) {
    throw "EasyProxy deployment failed with exit code $LASTEXITCODE"
}
