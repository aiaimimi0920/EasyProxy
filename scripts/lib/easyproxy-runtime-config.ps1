Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'easyproxy-common.ps1')

function Read-EasyProxyRuntimeConfig {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ConfigPath
    )

    Assert-EasyProxyCommand -Name 'python' -Hint 'Install Python 3 and PyYAML first.'
    $resolved = Resolve-EasyProxyPath -Path $ConfigPath
    $python = @'
import json
import pathlib
import sys

try:
    import yaml
except ImportError:
    print("PyYAML is required", file=sys.stderr)
    raise SystemExit(2)

path = pathlib.Path(sys.argv[1])
value = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
if not isinstance(value, dict):
    raise SystemExit("runtime config root must be a mapping")
print(json.dumps(value, ensure_ascii=False))
'@
    $json = $python | python - $resolved
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to parse runtime config: $resolved"
    }
    return ($json -join [Environment]::NewLine) | ConvertFrom-Json
}

function Get-EasyProxyRuntimeSection {
    param(
        [object]$Object,
        [Parameter(Mandatory = $true)][string]$Name
    )

    if ($null -eq $Object -or $null -eq $Object.PSObject.Properties[$Name]) {
        return $null
    }
    return $Object.$Name
}

function Get-EasyProxyRuntimeValue {
    param(
        [object]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        $Default = $null
    )

    if ($null -eq $Object -or $null -eq $Object.PSObject.Properties[$Name]) {
        return $Default
    }
    $value = $Object.$Name
    if ($null -eq $value -or ($value -is [string] -and [string]::IsNullOrWhiteSpace($value))) {
        return $Default
    }
    return $value
}
