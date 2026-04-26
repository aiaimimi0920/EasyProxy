param(
    [string]$OutputPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

$template = Resolve-EasyProxyPath -Path 'config.example.yaml'
Ensure-EasyProxyPathExists -Path $template -Message "Missing config template: $template"

$resolvedOutput = if ([System.IO.Path]::IsPathRooted($OutputPath)) {
    $OutputPath
} else {
    Join-Path (Get-EasyProxyRepoRoot) $OutputPath
}

if ((Test-Path -LiteralPath $resolvedOutput) -and -not $Force) {
    throw "Config already exists: $resolvedOutput. Pass -Force to overwrite it."
}

Copy-Item -LiteralPath $template -Destination $resolvedOutput -Force
Write-Host "Config initialized: $resolvedOutput"
