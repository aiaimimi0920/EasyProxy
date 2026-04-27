param(
    [string]$PublicKeyOutput = 'tmp/easyproxy_import_code_owner_public.txt',
    [string]$PrivateKeyOutput = 'tmp/easyproxy_import_code_owner_private.txt',
    [string]$BundleOutput = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')

Assert-EasyProxyCommand -Name "python" -Hint "Install Python 3 first."

$scriptPath = Join-Path $PSScriptRoot 'easyproxy-import-code.py'
$args = @(
    $scriptPath,
    'generate-keypair',
    '--public-key-output', (Join-Path (Get-EasyProxyRepoRoot) $PublicKeyOutput),
    '--private-key-output', (Join-Path (Get-EasyProxyRepoRoot) $PrivateKeyOutput)
)
if (-not [string]::IsNullOrWhiteSpace($BundleOutput)) {
    $args += @('--bundle-output', (Join-Path (Get-EasyProxyRepoRoot) $BundleOutput))
}

& python @args
if ($LASTEXITCODE -ne 0) {
    throw "Failed to generate import code keypair with exit code $LASTEXITCODE"
}
