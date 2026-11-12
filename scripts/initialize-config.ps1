param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('topology', 'runtime')]
    [string]$Target,
    [string]$OutputPath = '',
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

$label = 'Topology'
$templatePath = 'topology.example.yaml'
$defaultOutput = 'topology.yaml'
if ($Target -eq 'runtime') {
    $label = 'Runtime config'
    $templatePath = 'deploy/service/base/config.template.yaml'
    $defaultOutput = 'deploy/service/base/config.yaml'
}
if ([string]::IsNullOrWhiteSpace($OutputPath)) { $OutputPath = $defaultOutput }
$template = Resolve-EasyProxyPath -Path $templatePath
$resolvedOutput = if ([System.IO.Path]::IsPathRooted($OutputPath)) {
    [System.IO.Path]::GetFullPath($OutputPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path (Get-EasyProxyRepoRoot) $OutputPath))
}
if ((Test-Path -LiteralPath $resolvedOutput) -and -not $Force) {
    throw "$label already exists: $resolvedOutput. Pass -Force to overwrite it."
}
$parent = Split-Path -Parent $resolvedOutput
if (-not [string]::IsNullOrWhiteSpace($parent)) {
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
}
Copy-Item -LiteralPath $template -Destination $resolvedOutput -Force
Write-Host "$label initialized: $resolvedOutput"
