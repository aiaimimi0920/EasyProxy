Set-StrictMode -Version Latest

function Resolve-EasyProxyEnvValue {
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

function Read-EasyProxyGitCredential {
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
    }
    catch {
        return $null
    }
    finally {
        if (Test-Path $tempFile) {
            Remove-Item -Force $tempFile -ErrorAction SilentlyContinue
        }
    }
}

function Resolve-EasyProxyGhcrAuth {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ImagePrefix,

        [AllowEmptyString()]
        [string]$GhcrUsername,

        [AllowEmptyString()]
        [string]$GhcrToken,

        [string]$DefaultOwner = 'aiaimimi0920'
    )

    $resolvedUsername = Resolve-EasyProxyEnvValue -CurrentValue $GhcrUsername -EnvName 'GHCR_USERNAME'
    $resolvedToken = Resolve-EasyProxyEnvValue -CurrentValue $GhcrToken -EnvName 'GHCR_TOKEN'

    $gitCredential = $null
    if ([string]::IsNullOrWhiteSpace($resolvedToken) -or [string]::IsNullOrWhiteSpace($resolvedUsername)) {
        $gitCredential = Read-EasyProxyGitCredential -Protocol 'https' -CredentialHost 'github.com'
    }

    if ([string]::IsNullOrWhiteSpace($resolvedToken) -and $null -ne $gitCredential -and $gitCredential.ContainsKey('password')) {
        $resolvedToken = [string]$gitCredential['password']
    }

    if ([string]::IsNullOrWhiteSpace($resolvedUsername)) {
        if ($ImagePrefix -match '^ghcr\.io/(?<owner>[^/]+)$') {
            $resolvedUsername = $matches['owner']
        }
        elseif ($null -ne $gitCredential -and $gitCredential.ContainsKey('username')) {
            $candidate = [string]$gitCredential['username']
            if (-not [string]::IsNullOrWhiteSpace($candidate) -and $candidate -notmatch '^Personal Access Token$') {
                $resolvedUsername = $candidate
            }
        }
    }

    if ([string]::IsNullOrWhiteSpace($resolvedUsername)) {
        $resolvedUsername = $DefaultOwner
    }

    return [pscustomobject]@{
        Username = $resolvedUsername
        Token    = $resolvedToken
    }
}

function Invoke-EasyProxyGhcrBuildxPublish {
    param(
        [Parameter(Mandatory = $true)]
        [string]$RepoRoot,
        [Parameter(Mandatory = $true)]
        [string]$DockerfilePath,
        [Parameter(Mandatory = $true)]
        [string]$ImagePrefix,
        [Parameter(Mandatory = $true)]
        [string]$ImageName,
        [Parameter(Mandatory = $true)]
        [string]$ReleaseTag,
        [string]$Platform = 'linux/amd64',
        [AllowEmptyString()]
        [string]$GhcrUsername,
        [AllowEmptyString()]
        [string]$GhcrToken,
        [switch]$LoadOnly,
        [switch]$NoCache
    )

    if ([string]::IsNullOrWhiteSpace($ReleaseTag)) {
        throw "ReleaseTag must not be empty."
    }

    $auth = Resolve-EasyProxyGhcrAuth -ImagePrefix $ImagePrefix -GhcrUsername $GhcrUsername -GhcrToken $GhcrToken
    $fullImage = "${ImagePrefix}/${ImageName}:${ReleaseTag}"

    Write-Host "Building image: $fullImage"
    Write-Host "Context: $RepoRoot"
    Write-Host "Dockerfile: $DockerfilePath"
    Write-Host "Platform: $Platform"

    if (-not $LoadOnly -and -not [string]::IsNullOrWhiteSpace($auth.Token)) {
        Write-Host "Attempting docker login against ghcr.io with the provided credential source..."
        $auth.Token | docker login ghcr.io --username $auth.Username --password-stdin | Out-Host
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "docker login ghcr.io failed; continuing with the current Docker auth state."
        }
    }
    elseif (-not $LoadOnly) {
        Write-Host "No explicit GHCR token was provided; relying on the current Docker auth state."
    }

    $dockerArgs = @(
        "buildx", "build",
        "--file", $DockerfilePath,
        "--platform", $Platform,
        "--tag", $fullImage
    )

    if ($NoCache) {
        $dockerArgs += "--no-cache"
    }

    if ($LoadOnly) {
        $dockerArgs += "--load"
    }
    else {
        $dockerArgs += "--push"
    }

    $dockerArgs += $RepoRoot

    Write-Host ("Running: docker " + ($dockerArgs -join " "))
    & docker @dockerArgs
    if ($LASTEXITCODE -ne 0) {
        throw "docker buildx build failed with exit code $LASTEXITCODE"
    }

    Write-Host "Completed: $fullImage"
}
