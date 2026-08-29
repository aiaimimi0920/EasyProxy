function New-ForwardingProfile {
    param(
        [Parameter(Mandatory = $true)][bool]$Enabled,
        [ValidateSet("DIRECT", "PROXY")][string]$FinalPolicy = "PROXY"
    )

    return [ordered]@{
        schema_version    = 1
        enabled           = $Enabled
        default_strategy  = "stable"
        use_default_rules = $false
        final_policy      = $FinalPolicy
        rules             = @()
        rule_providers    = @()
        node_filter       = [ordered]@{
            countries = @()
            regions   = @()
            long_lived = $null
        }
        long_lived       = [ordered]@{
            min_uptime      = "2h"
            min_success_rate = 0.9
        }
        session          = [ordered]@{ ttl = "10m" }
    }
}

function Copy-JsonObject {
    param([Parameter(Mandatory = $true)]$Value)
    return ($Value | ConvertTo-Json -Depth 100 -Compress | ConvertFrom-Json)
}

function Set-SharedProfileEnabled {
    param(
        [Parameter(Mandatory = $true)][string]$ManagementBaseUrl,
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [Parameter(Mandatory = $true)]$Resource,
        [Parameter(Mandatory = $true)][bool]$Enabled
    )

    $profile = Copy-JsonObject -Value $Resource.profile
    $profile.enabled = $Enabled
    $requestHeaders = @{} + $Headers
    $requestHeaders["If-Match"] = '"' + [string]$Resource.revision + '"'
    return Invoke-JsonApi -Method PUT -Uri "$ManagementBaseUrl/api/local-server/profiles/shared" -Headers $requestHeaders -Body @{
        expected_revision = [int64]$Resource.revision
        profile = $profile
    }
}

function Put-DeviceProfile {
    param(
        [Parameter(Mandatory = $true)][string]$ManagementBaseUrl,
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [Parameter(Mandatory = $true)][string]$DeviceId,
        [Parameter(Mandatory = $true)]$Profile,
        [int64]$ExpectedRevision = 0
    )

    $requestHeaders = @{} + $Headers
    if ($ExpectedRevision -eq 0) {
        $requestHeaders["If-None-Match"] = "*"
    }
    else {
        $requestHeaders["If-Match"] = '"' + [string]$ExpectedRevision + '"'
    }
    return Invoke-JsonApi -Method PUT -Uri "$ManagementBaseUrl/api/local-server/devices/$DeviceId/profile" -Headers $requestHeaders -Body @{
        expected_revision = $ExpectedRevision
        profile = $Profile
    }
}

function Get-CounterSnapshot {
    param([Parameter(Mandatory = $true)][string]$Uri)
    return (Invoke-JsonApi -Method GET -Uri $Uri).Body
}

function Reset-Counter {
    param([Parameter(Mandatory = $true)][string]$Uri)
    $null = Invoke-JsonApi -Method POST -Uri $Uri
}

function Get-CounterTotal {
    param([Parameter(Mandatory = $true)]$Snapshot)
    $total = 0
    foreach ($property in $Snapshot.targets.PSObject.Properties) {
        $total += [int]$property.Value
    }
    return $total
}

function Invoke-ProxyRequest {
    param(
        [Parameter(Mandatory = $true)][string]$ClientName,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$DeviceId,
        [Parameter(Mandatory = $true)][string]$Password,
        [ValidateSet("HTTP", "CONNECT", "SOCKS5")][string]$Protocol = "HTTP",
        [string]$Path = "target"
    )

    $rawUsername = if ([string]::IsNullOrWhiteSpace($DeviceId)) { "easyproxy" } else { "easyproxy+dev=$DeviceId" }
    $username = [Uri]::EscapeDataString($rawUsername)
    $escapedPassword = [Uri]::EscapeDataString($Password)
    $target = "http://direct:8080/$Path"
    $common = @("--silent", "--show-error", "--max-time", "25", "--output", "-", "--write-out", "`n__HTTP_STATUS__:%{http_code}")
    switch ($Protocol) {
        "HTTP" {
            $proxy = "http://${username}:${escapedPassword}@easyproxy:22323"
            $arguments = $common + @("--proxy", $proxy, $target)
        }
        "CONNECT" {
            $proxy = "http://${username}:${escapedPassword}@easyproxy:22323"
            $arguments = $common + @("--proxy", $proxy, "--proxytunnel", $target)
        }
        "SOCKS5" {
            $proxy = "socks5h://${username}:${escapedPassword}@easyproxy:22323"
            $arguments = $common + @("--proxy", $proxy, $target)
        }
    }
    $result = Invoke-ClientContainerCurl -ClientName $ClientName -Arguments $arguments
    $match = [regex]::Match($result.Output, "__HTTP_STATUS__:(?<status>\d{3})")
    $statusCode = if ($match.Success) { [int]$match.Groups["status"].Value } else { 0 }
    return [pscustomobject]@{
        ExitCode   = $result.ExitCode
        StatusCode = $statusCode
        Output     = $result.Output
    }
}

function Assert-Check {
    param(
        [Parameter(Mandatory = $true)][System.Collections.Specialized.OrderedDictionary]$Checks,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Details
    )

    $Checks[$Name] = [ordered]@{ passed = $Condition; details = $Details }
    if (-not $Condition) {
        throw "assertion failed [$Name]: $Details"
    }
    Write-Host "[local-server-e2e] PASS $Name"
}

function Wait-ForLocalServer {
    param(
        [Parameter(Mandatory = $true)][string]$ManagementBaseUrl,
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [int]$TimeoutSeconds = 240
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-JsonApi -Method GET -Uri "$ManagementBaseUrl/api/local-server/status" -Headers $Headers
            if ($response.Body.dispatcher_ready) {
                return $response.Body
            }
        }
        catch {
        }
        Start-Sleep -Seconds 2
    }
    throw "Local Server did not become ready before timeout"
}

function Wait-ForAvailableNode {
    param(
        [Parameter(Mandatory = $true)][string]$ManagementBaseUrl,
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [int]$TimeoutSeconds = 240
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $response = Invoke-JsonApi -Method GET -Uri "$ManagementBaseUrl/api/nodes?only_available=1" -Headers $Headers
        if (@($response.Body.nodes).Count -gt 0) {
            return $response.Body
        }
        Start-Sleep -Seconds 3
    }
    throw "counted upstream node did not become available before timeout"
}
