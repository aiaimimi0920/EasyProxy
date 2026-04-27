param(
    [Parameter(Mandatory = $true)]
    [string]$Image
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$output = & docker run --rm $Image -h 2>&1
if ($LASTEXITCODE -ne 0) {
    throw "ech-workers help smoke failed with exit code $LASTEXITCODE"
}

$text = [string]::Join([Environment]::NewLine, @($output))
if ($text -notmatch 'Usage|flag|help|-h') {
    throw "ech-workers help smoke output did not contain expected usage text."
}

Write-Host "[ech-workers-smoke] success"
