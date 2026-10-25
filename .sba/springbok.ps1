param(
    [Parameter(Mandatory = $true)][string]$RequestPath,
    [Parameter(Mandatory = $true)][string]$ResultPath
)
$ErrorActionPreference = 'Stop'
$env:PYTHONDONTWRITEBYTECODE = '1'
$env:PYTHONNOUSERSITE = '1'
$env:PYTHONUTF8 = '1'
& python3.12 -B -s (Join-Path $PSScriptRoot 'bootstrap.py') --request $RequestPath --result $ResultPath
exit $LASTEXITCODE
