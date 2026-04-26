Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot "easyproxy-common.ps1")

function Read-EasyProxyConfig {
    param(
        [string]$ConfigPath = (Join-Path (Get-EasyProxyRepoRoot) 'config.yaml')
    )

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
