param(
    [string]$OutputPath = (Join-Path $PSScriptRoot '..\topology.yaml'),
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

$template = Resolve-EasyProxyPath -Path 'topology.example.yaml'
$resolvedOutput = if ([System.IO.Path]::IsPathRooted($OutputPath)) {
    [System.IO.Path]::GetFullPath($OutputPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path (Get-EasyProxyRepoRoot) $OutputPath))
}
if ((Test-Path -LiteralPath $resolvedOutput) -and -not $Force) {
    throw "Topology already exists: $resolvedOutput. Pass -Force to overwrite it."
}
$parent = Split-Path -Parent $resolvedOutput
if (-not [string]::IsNullOrWhiteSpace($parent)) {
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
}
Copy-Item -LiteralPath $template -Destination $resolvedOutput -Force
Write-Host "Topology initialized: $resolvedOutput"
