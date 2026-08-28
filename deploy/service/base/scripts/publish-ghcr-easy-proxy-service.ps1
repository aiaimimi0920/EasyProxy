[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReleaseTag,
    [string]$ImagePrefix = "ghcr.io/aiaimimi0920",
    [string]$ImageName = "easy-proxy-monorepo-service",
    [string]$GhcrUsername = $env:GHCR_USERNAME,
    [string]$Platform = "linux/amd64",
    [switch]$LoadOnly,
    [switch]$NoCache
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RepoRoot {
    return (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path
}

$repoRoot = Get-RepoRoot
. (Join-Path $repoRoot "scripts\lib\easyproxy-ghcr.ps1")
$dockerfilePath = Join-Path $repoRoot "deploy\service\base\Dockerfile"

Invoke-EasyProxyGhcrBuildxPublish `
    -RepoRoot $repoRoot `
    -DockerfilePath $dockerfilePath `
    -ImagePrefix $ImagePrefix `
    -ImageName $ImageName `
    -ReleaseTag $ReleaseTag `
    -Platform $Platform `
    -GhcrUsername $GhcrUsername `
    -LoadOnly:$LoadOnly `
    -NoCache:$NoCache
