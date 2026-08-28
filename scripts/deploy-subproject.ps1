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
    [string]$Project,
    [string]$TopologyPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [string]$RuntimeConfigPath = (Join-Path $PSScriptRoot '..\deploy\service\base\config.yaml'),
    [switch]$InitTopology,
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
    [string]$ContainerName = '',
    [string]$PoolPortBinding = '',
    [string]$ManagementPortBinding = '',
    [string]$MultiPortBinding = '',
    [string]$NetworkAlias = '',
    [string]$ComposeProjectName = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-topology.ps1')

function Invoke-RepositoryScript {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,
        [string[]]$Arguments = @()
    )

    $scriptPath = Join-Path $PSScriptRoot $Name
    Invoke-EasyProxyExternalCommand `
        -FilePath 'powershell' `
        -Arguments (@('-ExecutionPolicy', 'Bypass', '-File', $scriptPath) + $Arguments) `
        -FailureMessage "$Name failed"
}

if ([string]::IsNullOrWhiteSpace($Project)) {
    throw 'Missing -Project. Use a value listed by the Project ValidateSet.'
}

$resolvedTopologyPath = if ([System.IO.Path]::IsPathRooted($TopologyPath)) {
    [System.IO.Path]::GetFullPath($TopologyPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path (Get-EasyProxyRepoRoot) $TopologyPath))
}
if (-not (Test-Path -LiteralPath $resolvedTopologyPath)) {
    if (-not $InitTopology) {
        throw "Missing topology: $resolvedTopologyPath. Copy topology.example.yaml or pass -InitTopology."
    }
    Copy-Item -LiteralPath (Resolve-EasyProxyPath -Path 'topology.example.yaml') -Destination $resolvedTopologyPath
}
$null = Read-EasyProxyTopology -TopologyPath $resolvedTopologyPath

switch ($Project) {
    { $_ -in @('easyproxy', 'easyproxy-ghcr') } {
        $arguments = @('-TopologyPath', $resolvedTopologyPath, '-RuntimeConfigPath', $RuntimeConfigPath)
        if ($Project -eq 'easyproxy-ghcr') { $arguments += '-FromGhcr' }
        if (-not [string]::IsNullOrWhiteSpace($ImportCode)) { $arguments += @('-ImportCode', $ImportCode) }
        if (-not [string]::IsNullOrWhiteSpace($BootstrapFile)) { $arguments += @('-BootstrapFile', $BootstrapFile) }
        if ($NoBuild) { $arguments += '-NoBuild' }
        if (-not [string]::IsNullOrWhiteSpace($ReleaseTag)) { $arguments += @('-ReleaseTag', $ReleaseTag) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrOwner)) { $arguments += @('-GhcrOwner', $GhcrOwner) }
        if (-not [string]::IsNullOrWhiteSpace($Image)) { $arguments += @('-Image', $Image) }
        if ($SkipPull) { $arguments += '-SkipPull' }
        foreach ($pair in @(
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
        Invoke-RepositoryScript -Name 'deploy-easyproxy.ps1' -Arguments $arguments
    }
    { $_ -in @('misub-pages', 'misub-docker') } {
        $mode = if ($Project -eq 'misub-pages') { 'pages' } else { 'docker' }
        $arguments = @('-TopologyPath', $resolvedTopologyPath, '-Mode', $mode)
        if ($NoInstall) { $arguments += '-NoInstall' }
        if ($NoBuild) { $arguments += '-NoBuild' }
        Invoke-RepositoryScript -Name 'deploy-misub.ps1' -Arguments $arguments
    }
    'aggregator' {
        $arguments = @('-TopologyPath', $resolvedTopologyPath)
        if ($SkipWorkflowTrigger) { $arguments += '-SkipWorkflowTrigger' }
        Invoke-RepositoryScript -Name 'deploy-aggregator.ps1' -Arguments $arguments
    }
    'ech-workers-cloudflare' {
        $arguments = @('-TopologyPath', $resolvedTopologyPath)
        if ($DryRun) { $arguments += '-DryRun' }
        if ($SkipSecretSync) { $arguments += '-SkipSecretSync' }
        Invoke-RepositoryScript -Name 'deploy-ech-workers-cloudflare.ps1' -Arguments $arguments
    }
    { $_ -in @('build-easyproxy-image', 'build-ech-workers-image') } {
        $name = if ($Project -eq 'build-easyproxy-image') { 'build-easyproxy-image.ps1' } else { 'build-ech-workers-image.ps1' }
        $arguments = @()
        if (-not [string]::IsNullOrWhiteSpace($Image)) { $arguments += @('-Image', $Image) }
        if ($NoCache) { $arguments += '-NoCache' }
        if ($Push) { $arguments += '-Push' }
        Invoke-RepositoryScript -Name $name -Arguments $arguments
    }
    'publish-service-base-config' {
        $arguments = @('-TopologyPath', $resolvedTopologyPath, '-RuntimeConfigPath', $RuntimeConfigPath)
        if (-not [string]::IsNullOrWhiteSpace($ReleaseTag)) { $arguments += @('-ReleaseVersion', $ReleaseTag) }
        Invoke-RepositoryScript -Name 'publish-service-base-config.ps1' -Arguments $arguments
    }
    { $_ -in @('publish-easyproxy-image', 'publish-ech-workers-image', 'publish-core-images') } {
        $target = switch ($Project) {
            'publish-easyproxy-image' { 'easyproxy' }
            'publish-ech-workers-image' { 'ech-workers' }
            default { 'both' }
        }
        $arguments = @('-Target', $target)
        foreach ($pair in @(
            @('ReleaseTag', $ReleaseTag),
            @('GhcrOwner', $GhcrOwner),
            @('GhcrUsername', $GhcrUsername)
        )) {
            if (-not [string]::IsNullOrWhiteSpace([string]$pair[1])) {
                $arguments += @("-$($pair[0])", [string]$pair[1])
            }
        }
        if ($NoCache) { $arguments += '-NoCache' }
        if ($LoadOnly) { $arguments += '-LoadOnly' }
        Invoke-RepositoryScript -Name 'publish-ghcr-images.ps1' -Arguments $arguments
    }
}

Write-Host "Done: $Project" -ForegroundColor Green
