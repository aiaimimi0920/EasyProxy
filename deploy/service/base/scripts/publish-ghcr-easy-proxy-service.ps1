[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReleaseTag,
    [string]$ImagePrefix = "ghcr.io/aiaimimi0920",
    [string]$ImageName = "easy-proxy-monorepo-service",
    [string]$GhcrUsername = $env:GHCR_USERNAME,
    [string]$GhcrToken = $env:GHCR_TOKEN,
    [switch]$LoadOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RepoRoot {
    return (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path
}

function Resolve-EnvValue {
    param(
        [AllowEmptyString()]
        [string]$CurrentValue,

        [Parameter(Mandatory = $true)]
        [string]$EnvName
    )

    if (-not [string]::IsNullOrWhiteSpace($CurrentValue)) {
        return $CurrentValue
    }

    $userValue = [System.Environment]::GetEnvironmentVariable($EnvName, 'User')
    if (-not [string]::IsNullOrWhiteSpace($userValue)) {
        return $userValue
    }

    $machineValue = [System.Environment]::GetEnvironmentVariable($EnvName, 'Machine')
    if (-not [string]::IsNullOrWhiteSpace($machineValue)) {
        return $machineValue
    }

    return $CurrentValue
}

function Read-GitCredential {
    param(
        [string]$Protocol = 'https',
        [string]$CredentialHost = 'github.com'
    )

    $tempFile = Join-Path $env:TEMP ("git-credential-fill-" + [Guid]::NewGuid().ToString('N') + ".txt")
    try {
        @(
            "protocol=$Protocol",
            "host=$CredentialHost",
            ""
        ) | Set-Content -Path $tempFile -Encoding ASCII

        $output = cmd /c "type `"$tempFile`" | git credential fill" 2>$null
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($output)) {
            return $null
        }

        $credential = @{}
        foreach ($line in ($output -split "`r?`n")) {
            if ($line -match '^(?<key>[^=]+)=(?<value>.*)$') {
                $credential[$matches['key']] = $matches['value']
            }
        }

        if ([string]::IsNullOrWhiteSpace($credential['password'])) {
            return $null
        }

        return $credential
    } catch {
        return $null
    } finally {
        if (Test-Path $tempFile) {
            Remove-Item -Force $tempFile -ErrorAction SilentlyContinue
        }
    }
}

$repoRoot = Get-RepoRoot
$dockerfilePath = Join-Path $repoRoot "deploy\service\base\Dockerfile"

$GhcrUsername = Resolve-EnvValue -CurrentValue $GhcrUsername -EnvName 'GHCR_USERNAME'
$GhcrToken = Resolve-EnvValue -CurrentValue $GhcrToken -EnvName 'GHCR_TOKEN'

$gitCredential = $null
if ([string]::IsNullOrWhiteSpace($GhcrToken) -or [string]::IsNullOrWhiteSpace($GhcrUsername)) {
    $gitCredential = Read-GitCredential -Protocol 'https' -CredentialHost 'github.com'
}

if ([string]::IsNullOrWhiteSpace($GhcrToken) -and $null -ne $gitCredential -and $gitCredential.ContainsKey('password')) {
    $GhcrToken = [string]$gitCredential['password']
}

if ([string]::IsNullOrWhiteSpace($GhcrUsername)) {
    if ($ImagePrefix -match '^ghcr\.io/(?<owner>[^/]+)$') {
        $GhcrUsername = $matches['owner']
    } elseif ($null -ne $gitCredential -and $gitCredential.ContainsKey('username')) {
        $candidate = [string]$gitCredential['username']
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and $candidate -notmatch '^Personal Access Token$') {
            $GhcrUsername = $candidate
        }
    }
}

if ([string]::IsNullOrWhiteSpace($GhcrUsername)) {
    $GhcrUsername = 'aiaimimi0920'
}

$fullImage = "${ImagePrefix}/${ImageName}:${ReleaseTag}"

Write-Host "Building image: $fullImage"
Write-Host "Context: $repoRoot"
Write-Host "Dockerfile: $dockerfilePath"

if (-not $LoadOnly -and -not [string]::IsNullOrWhiteSpace($GhcrToken)) {
    Write-Host "Attempting docker login against ghcr.io with the provided credential source..."
    $GhcrToken | docker login ghcr.io --username $GhcrUsername --password-stdin | Out-Host
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "docker login ghcr.io failed; continuing with the current Docker auth state."
    }
} elseif (-not $LoadOnly) {
    Write-Host "No explicit GHCR token was provided; relying on the current Docker auth state."
}

$dockerArgs = @(
    "buildx", "build",
    "--file", $dockerfilePath,
    "--platform", "linux/amd64",
    "--tag", $fullImage
)

if ($LoadOnly) {
    $dockerArgs += "--load"
} else {
    $dockerArgs += "--push"
}

$dockerArgs += $repoRoot

Write-Host ("Running: docker " + ($dockerArgs -join " "))
& docker @dockerArgs
if ($LASTEXITCODE -ne 0) {
    throw "docker buildx build failed with exit code $LASTEXITCODE"
}

Write-Host "Completed: $fullImage"
