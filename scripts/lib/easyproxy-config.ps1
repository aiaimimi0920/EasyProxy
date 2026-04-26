Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot "easyproxy-common.ps1")

function Assert-EasyProxyPythonYaml {
    Assert-EasyProxyCommand -Name "python" -Hint "Install Python 3 first."

    $probe = @'
import importlib.util
import sys

if importlib.util.find_spec("yaml") is None:
    print("PyYAML package not found", file=sys.stderr)
    raise SystemExit(2)
'@

    $null = $probe | python -
    if ($LASTEXITCODE -ne 0) {
        throw "Python package 'PyYAML' is required. Install it with: python -m pip install pyyaml"
    }
}

function Read-EasyProxyConfig {
    param(
        [string]$ConfigPath = (Join-Path (Get-EasyProxyRepoRoot) 'config.yaml')
    )

    Assert-EasyProxyPythonYaml

    $resolvedConfigPath = Resolve-EasyProxyPath -Path $ConfigPath
    if (-not (Test-Path -LiteralPath $resolvedConfigPath)) {
        throw "Config file not found: $resolvedConfigPath. Copy config.example.yaml to config.yaml first."
    }

    $python = @'
import json
import pathlib
import sys
import yaml

config_path = pathlib.Path(sys.argv[1])
payload = yaml.safe_load(config_path.read_text(encoding="utf-8")) or {}
print(json.dumps(payload, ensure_ascii=False))
'@

    $json = $python | python - $resolvedConfigPath
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to parse YAML config: $resolvedConfigPath"
    }

    if ([string]::IsNullOrWhiteSpace($json)) {
        return [pscustomobject]@{}
    }

    return $json | ConvertFrom-Json
}

function Get-EasyProxyConfigSection {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Config,
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $property = $Config.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $null
    }

    return $Config.$Name
}

function Get-EasyProxyConfigValue {
    param(
        [object]$Object,
        [Parameter(Mandatory = $true)]
        [string]$Name,
        $Default = $null
    )

    if ($null -eq $Object) {
        return $Default
    }

    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $Default
    }

    $value = $Object.$Name
    if ($null -eq $value) {
        return $Default
    }

    if ($value -is [string] -and [string]::IsNullOrWhiteSpace($value)) {
        return $Default
    }

    return $value
}
