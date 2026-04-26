Set-StrictMode -Version Latest

$script:EasyProxyRepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path

function Get-EasyProxyRepoRoot {
    return $script:EasyProxyRepoRoot
}

function Resolve-EasyProxyPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Path must not be empty."
    }

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return (Resolve-Path -LiteralPath $Path).Path
    }

    return (Resolve-Path -LiteralPath (Join-Path (Get-EasyProxyRepoRoot) $Path)).Path
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
