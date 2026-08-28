Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'easyproxy-common.ps1')

function Invoke-EasyProxyCtl {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    $repoRoot = Get-EasyProxyRepoRoot
    $configured = [string]$env:EASYPROXYCTL
    $bundledName = if ($env:OS -eq 'Windows_NT') { 'easyproxyctl.exe' } else { 'easyproxyctl' }
    $bundled = Join-Path $repoRoot "tools/easyproxyctl/bin/$bundledName"

    if (-not [string]::IsNullOrWhiteSpace($configured)) {
        $output = & $configured @Arguments
    }
    elseif (Test-Path -LiteralPath $bundled) {
        $output = & $bundled @Arguments
    }
    elseif ($null -ne (Get-Command easyproxyctl -ErrorAction SilentlyContinue)) {
        $output = & easyproxyctl @Arguments
    }
    elseif ($null -ne (Get-Command go -ErrorAction SilentlyContinue)) {
        $toolRoot = Join-Path $repoRoot 'tools/easyproxyctl'
        Push-Location $toolRoot
        try {
            $output = & go run ./cmd/easyproxyctl @Arguments
        }
        finally {
            Pop-Location
        }
    }
    else {
        throw 'easyproxyctl is unavailable. Install a release binary or Go 1.24+.'
    }

    if ($LASTEXITCODE -ne 0) {
        throw "easyproxyctl failed with exit code $LASTEXITCODE"
    }
    return ($output -join [Environment]::NewLine)
}

function Read-EasyProxyTopology {
    param(
        [string]$TopologyPath = (Join-Path (Get-EasyProxyRepoRoot) 'topology.yaml')
    )

    $resolved = Resolve-EasyProxyPath -Path $TopologyPath
    $json = Invoke-EasyProxyCtl -Arguments @('topology', 'show', '--file', $resolved)
    return $json | ConvertFrom-Json
}

function Get-EasyProxyResourceNames {
    param(
        [string]$TopologyPath = (Join-Path (Get-EasyProxyRepoRoot) 'topology.yaml')
    )

    $resolved = Resolve-EasyProxyPath -Path $TopologyPath
    $json = Invoke-EasyProxyCtl -Arguments @('topology', 'names', '--file', $resolved)
    return $json | ConvertFrom-Json
}

function Get-EasyProxyEnvironmentValue {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Reference,
        [Parameter(Mandatory = $true)]
        [string]$Purpose,
        [switch]$Optional
    )

    if ([string]::IsNullOrWhiteSpace($Reference)) {
        if ($Optional) { return '' }
        throw "Missing environment variable reference for $Purpose."
    }
    $value = [Environment]::GetEnvironmentVariable($Reference)
    if ([string]::IsNullOrWhiteSpace($value) -and -not $Optional) {
        throw "Environment variable $Reference is required for $Purpose."
    }
    return [string]$value
}
