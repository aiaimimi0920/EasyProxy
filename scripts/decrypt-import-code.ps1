param(
    [Parameter(Mandatory = $true)]
    [string]$EncryptedFilePath,
    [Parameter(Mandatory = $true)]
    [string]$PrivateKeyPath,
    [switch]$ImportCodeOnly,
    [string]$OutputPath = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

function Resolve-EasyProxyOutputPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Output path must not be empty."
    }

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }

    return [System.IO.Path]::GetFullPath((Join-Path (Get-EasyProxyRepoRoot) $Path))
}

Assert-EasyProxyCommand -Name "python" -Hint "Install Python 3 first."

$scriptPath = Join-Path $PSScriptRoot 'easyproxy-import-code.py'
$args = @(
    $scriptPath,
    'decrypt',
    '--encrypted-file', (Resolve-EasyProxyPath -Path $EncryptedFilePath),
    '--private-key-file', (Resolve-EasyProxyPath -Path $PrivateKeyPath)
)
if ($ImportCodeOnly) {
    $args += '--import-code-only'
}
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $args += @('--output', (Resolve-EasyProxyOutputPath -Path $OutputPath))
}

& python @args
if ($LASTEXITCODE -ne 0) {
    throw "Failed to decrypt import code with exit code $LASTEXITCODE"
}
