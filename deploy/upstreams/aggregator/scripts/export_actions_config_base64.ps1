param(
    [string]$ConfigPath = "",
    [switch]$NoNewline
)

$ErrorActionPreference = "Stop"

$deployRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
if (-not $ConfigPath) {
    $ConfigPath = Join-Path $deployRoot "config\config.actions.r2.json"
}

$resolvedConfigPath = Resolve-Path $ConfigPath
$content = Get-Content -LiteralPath $resolvedConfigPath -Raw -Encoding UTF8
$bytes = [System.Text.Encoding]::UTF8.GetBytes($content)
$encoded = [Convert]::ToBase64String($bytes)

# Emit through the success pipeline so callers can safely capture the result
# from the current PowerShell session as well as from a child process.
Write-Output $encoded
