param(
    [Parameter(Mandatory = $true)]
    [string]$Image
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$output = @()
$exitCode = -1
$previousErrorActionPreference = $ErrorActionPreference
try {
    # Windows PowerShell surfaces native stderr as ErrorRecord objects even
    # when the process exits successfully. Go's flag package writes help there.
    $ErrorActionPreference = "Continue"
    $output = & docker run --rm $Image -h 2>&1
    $exitCode = $LASTEXITCODE
}
finally {
    $ErrorActionPreference = $previousErrorActionPreference
}

if ($exitCode -ne 0) {
    throw "ech-workers help smoke failed with exit code $exitCode"
}

$text = [string]::Join([Environment]::NewLine, @($output))
if ($text -notmatch 'Usage|flag|help|-h') {
    throw "ech-workers help smoke output did not contain expected usage text."
}

Write-Host "[ech-workers-smoke] success"
