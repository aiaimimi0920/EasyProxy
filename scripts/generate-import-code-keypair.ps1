param(
    [string]$PublicKeyOutput = 'tmp/easyproxy_import_code_owner_public.txt',
    [string]$PrivateKeyOutput = 'tmp/easyproxy_import_code_owner_private.txt',
    [string]$BundleOutput = ''
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
    'generate-keypair',
    '--public-key-output', (Resolve-EasyProxyOutputPath -Path $PublicKeyOutput),
    '--private-key-output', (Resolve-EasyProxyOutputPath -Path $PrivateKeyOutput)
)
if (-not [string]::IsNullOrWhiteSpace($BundleOutput)) {
    $args += @('--bundle-output', (Resolve-EasyProxyOutputPath -Path $BundleOutput))
}

& python @args
if ($LASTEXITCODE -ne 0) {
    throw "Failed to generate import code keypair with exit code $LASTEXITCODE"
}
