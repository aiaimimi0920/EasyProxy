Set-StrictMode -Version Latest

$script:EasyProxyRepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path

function Get-EasyProxyRepoRoot {
    return $script:EasyProxyRepoRoot
}

function Resolve-EasyProxyPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [switch]$AllowMissing
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Path must not be empty."
    }

    $candidate = if ([System.IO.Path]::IsPathRooted($Path)) {
        $Path
    } else {
        Join-Path (Get-EasyProxyRepoRoot) $Path
    }

    if ($AllowMissing) {
        return [System.IO.Path]::GetFullPath($candidate)
    }

    return (Resolve-Path -LiteralPath $candidate).Path
}

function Ensure-EasyProxyPathExists {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [string]$Message = ""
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        if ([string]::IsNullOrWhiteSpace($Message)) {
            throw "Missing path: $Path"
        }
        throw $Message
    }
}

function Assert-EasyProxyCommand {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,
        [string]$Hint = ""
    )

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        if ([string]::IsNullOrWhiteSpace($Hint)) {
            throw "Required command not found: $Name"
        }
        throw "Required command not found: $Name. $Hint"
    }
}

function Invoke-EasyProxyExternalCommand {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,
        [string[]]$Arguments = @(),
        [string]$WorkingDirectory = "",
        [string]$FailureMessage = ""
    )

    $previous = $null
    if (-not [string]::IsNullOrWhiteSpace($WorkingDirectory)) {
        $previous = Get-Location
        Push-Location $WorkingDirectory
    }

    try {
        $capturePath = [System.Environment]::GetEnvironmentVariable('EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH')
        if (-not [string]::IsNullOrWhiteSpace($capturePath)) {
            $record = [pscustomobject]@{
                FilePath         = $FilePath
                Arguments        = @($Arguments)
                WorkingDirectory = $WorkingDirectory
                FailureMessage   = $FailureMessage
            }
            $line = $record | ConvertTo-Json -Compress -Depth 5
            Add-Content -LiteralPath $capturePath -Value $line -Encoding UTF8
            return
        }

        & $FilePath @Arguments
        if ($LASTEXITCODE -ne 0) {
            if ([string]::IsNullOrWhiteSpace($FailureMessage)) {
                throw ("Command failed with exit code {0}: {1} {2}" -f $LASTEXITCODE, $FilePath, ($Arguments -join ' '))
            }
            throw "$FailureMessage (exit code $LASTEXITCODE)"
        }
    }
    finally {
        if ($null -ne $previous) {
            Pop-Location
        }
    }
}

function Get-EasyProxyJsoncStringProperty {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $content = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
    $pattern = '"{0}"\s*:\s*"(?<value>[^"]+)"' -f [Regex]::Escape($Name)
    $match = [Regex]::Match($content, $pattern)
    if (-not $match.Success) {
        return ""
    }

    return $match.Groups["value"].Value
}
