function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}
function Invoke-Docker {
    param([Parameter(Mandatory = $true)][string[]]$DockerArgs)

    $previousErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = @(& docker @DockerArgs 2>&1)
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($exitCode -ne 0) {
        throw "docker $($DockerArgs -join ' ') failed:`n$($output -join [Environment]::NewLine)"
    }
    return $output
}

function Invoke-JsonApi {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Uri,
        [hashtable]$Headers = @{},
        [object]$Body = $null,
        [switch]$AllowFailure
    )

    $invokeParams = @{
        Method     = $Method
        Uri        = $Uri
        TimeoutSec = 30
    }
    if ($Headers.Count -gt 0) {
        $invokeParams.Headers = $Headers
    }
    if ($null -ne $Body) {
        $invokeParams.ContentType = "application/json"
        $invokeParams.Body = ($Body | ConvertTo-Json -Depth 100 -Compress)
    }

    try {
        $payload = Invoke-RestMethod @invokeParams
        return [pscustomobject]@{
            StatusCode = 200
            Body       = $payload
        }
    }
    catch {
        $statusCode = 0
        $payload = $null
        $response = $_.Exception.Response
        if ($null -ne $response -and $null -ne $response.StatusCode) {
            $statusCode = [int]$response.StatusCode
        }
        $errorText = if ($null -ne $_.ErrorDetails) {
            [string]$_.ErrorDetails.Message
        }
        else {
            ""
        }
        if ([string]::IsNullOrWhiteSpace($errorText) -and $null -ne $response) {
            try {
                $reader = [System.IO.StreamReader]::new($response.GetResponseStream())
                $errorText = $reader.ReadToEnd()
                $reader.Dispose()
            }
            catch {
                $errorText = ""
            }
        }
        if (-not [string]::IsNullOrWhiteSpace($errorText)) {
            try {
                $payload = $errorText | ConvertFrom-Json
            }
            catch {
                $payload = [pscustomobject]@{ error = $errorText }
            }
        }
        if (-not $AllowFailure) {
            throw
        }
        return [pscustomobject]@{
            StatusCode = $statusCode
            Body       = $payload
        }
    }
}

function Invoke-ClientContainerCurl {
    param(
        [Parameter(Mandatory = $true)][string]$ClientName,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    $previousErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = @(& docker exec $ClientName curl @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    return [pscustomobject]@{
        ExitCode = $exitCode
        Output   = ($output -join "`n")
    }
}

function Get-LegacyContainerInvariant {
    $containerId = [string](@(& docker ps -a --filter "name=^/easy-proxy$" --format "{{.ID}}" 2>$null) | Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($containerId)) {
        return [ordered]@{ exists = $false }
    }
    $inspect = ((& docker inspect easy-proxy 2>$null) -join "`n") | ConvertFrom-Json
    $item = @($inspect)[0]
    return [ordered]@{
        exists     = $true
        id         = [string]$item.Id
        image      = [string]$item.Config.Image
        status     = [string]$item.State.Status
        exitCode   = [int]$item.State.ExitCode
        finishedAt = [string]$item.State.FinishedAt
    }
}

function Assert-LegacyContainerInvariant {
    param(
        [Parameter(Mandatory = $true)]$Before,
        [Parameter(Mandatory = $true)]$After
    )

    foreach ($field in @("exists", "id", "image", "status", "exitCode", "finishedAt")) {
        $beforeValue = if ($Before -is [System.Collections.IDictionary]) {
            if ($Before.Contains($field)) { $Before[$field] } else { $null }
        }
        else {
            $beforeProperty = $Before.PSObject.Properties[$field]
            if ($null -eq $beforeProperty) { $null } else { $beforeProperty.Value }
        }
        $afterValue = if ($After -is [System.Collections.IDictionary]) {
            if ($After.Contains($field)) { $After[$field] } else { $null }
        }
        else {
            $afterProperty = $After.PSObject.Properties[$field]
            if ($null -eq $afterProperty) { $null } else { $afterProperty.Value }
        }
        if ($beforeValue -ne $afterValue) {
            throw "legacy easy-proxy invariant changed for ${field}: before=$beforeValue after=$afterValue"
        }
    }
}

function Remove-DisposableTopology {
    param(
        [Parameter(Mandatory = $true)][string]$ValidationId,
        [object]$Metadata = $null
    )

    if ($null -ne $Metadata) {
        $metadataId = [string]$Metadata.validationId
        if ($metadataId -ne $ValidationId) {
            throw "cleanup metadata validationId mismatch: $metadataId != $ValidationId"
        }
    }
    $label = "easyproxy.validation-id=$ValidationId"
    $containerIds = @(& docker ps -aq --filter "label=$label" 2>$null | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($containerIds.Count -gt 0) {
        $null = Invoke-Docker -DockerArgs (@("rm", "-f") + $containerIds)
    }
    $networkIds = @(& docker network ls -q --filter "label=$label" 2>$null | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($networkIds.Count -gt 0) {
        $null = Invoke-Docker -DockerArgs (@("network", "rm") + $networkIds)
    }
    $volumeNames = @(& docker volume ls -q --filter "label=$label" 2>$null | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($volumeNames.Count -gt 0) {
        $null = Invoke-Docker -DockerArgs (@("volume", "rm", "-f") + $volumeNames)
    }
}

function Write-Utf8NoBom {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Value
    )

    [System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($Path)) | Out-Null
    [System.IO.File]::WriteAllText($Path, $Value, [System.Text.UTF8Encoding]::new($false))
}

function New-BasicHeaders {
    param(
        [Parameter(Mandatory = $true)][string]$Username,
        [Parameter(Mandatory = $true)][string]$Password
    )

    $raw = [System.Text.Encoding]::UTF8.GetBytes("${Username}:${Password}")
    return @{ Authorization = "Basic $([Convert]::ToBase64String($raw))" }
}
