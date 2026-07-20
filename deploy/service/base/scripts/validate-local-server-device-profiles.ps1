[CmdletBinding()]
param(
    [string]$Image = "",
    [string]$ValidationId = ("local-server-" + (Get-Date -Format "yyyyMMdd-HHmmss")),
    [switch]$KeepArtifacts,
    [switch]$KeepRuntime,
    [switch]$CleanupOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

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
        $errorText = [string]$_.ErrorDetails.Message
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

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = (Resolve-Path (Join-Path $scriptDir "..\..\..\..")).Path
$artifactRoot = Join-Path $repoRoot "tmp\local-server-device-profile-validation"
$artifactDir = Join-Path $artifactRoot $ValidationId
$metadataPath = Join-Path $artifactDir "topology.json"
$evidencePath = Join-Path $artifactDir "evidence.json"
$fixturePath = Join-Path $scriptDir "local-server-e2e-fixture.py"

if ($CleanupOnly) {
    if (-not (Test-Path -LiteralPath $metadataPath)) {
        throw "cleanup metadata not found: $metadataPath"
    }
    $metadata = Get-Content -LiteralPath $metadataPath -Raw -Encoding UTF8 | ConvertFrom-Json
    Remove-DisposableTopology -ValidationId $ValidationId -Metadata $metadata
    $legacyAfterCleanup = Get-LegacyContainerInvariant
    Assert-LegacyContainerInvariant -Before $metadata.legacyBefore -After $legacyAfterCleanup
    Write-Host "[local-server-e2e] cleanup complete for $ValidationId"
    return
}

if ([string]::IsNullOrWhiteSpace($Image)) {
    $Image = "easyproxy/easy-proxy-monorepo-service:local"
}

$safeId = (($ValidationId -replace "[^A-Za-z0-9]", "").ToLowerInvariant())
if ([string]::IsNullOrWhiteSpace($safeId)) {
    throw "ValidationId must contain at least one alphanumeric character"
}
if ($safeId.Length -gt 32) {
    $safeId = $safeId.Substring(0, 32)
}
$prefix = "epls-$safeId"
$networkName = "$prefix-net"
$volumeName = "$prefix-data"
$originName = "$prefix-origin"
$upstreamName = "$prefix-upstream"
$serviceName = "$prefix-service"
$clientName = "$prefix-client"
$label = "easyproxy.validation-id=$ValidationId"
$managementPort = Get-FreeTcpPort
$proxyPort = Get-FreeTcpPort
$originPort = Get-FreeTcpPort
$upstreamCounterPort = Get-FreeTcpPort
$configDir = Join-Path $artifactDir "config"
$configPath = Join-Path $configDir "config.yaml"
$initialUsername = "easyproxy"
$initialPassword = "local-server-e2e-secret"
$rotatedPassword = "local-server-e2e-rotated-secret"
$managementBaseUrl = "http://127.0.0.1:$managementPort"
$originCounterUrl = "http://127.0.0.1:$originPort/counter"
$originResetUrl = "http://127.0.0.1:$originPort/counter/reset"
$upstreamCounterUrl = "http://127.0.0.1:$upstreamCounterPort/counter"
$upstreamResetUrl = "http://127.0.0.1:$upstreamCounterPort/counter/reset"
$legacyBefore = Get-LegacyContainerInvariant
$checks = [ordered]@{}
$failure = $null
$legacyAfter = $null

$configYaml = @"
mode: pool
log_level: info
skip_cert_verify: true
database_path: /var/lib/easy-proxy/data/data.db

listener:
  address: 0.0.0.0
  port: 22323
  protocol: mixed
  username: $initialUsername
  password: $initialPassword

pool:
  mode: auto
  failure_threshold: 3
  blacklist_duration: 30s

routing:
  enabled: true
  listen: ""
  default_strategy: stable
  use_default_rules: false
  final_policy: PROXY
  rules: []
  rule_providers: []
  long_lived:
    min_uptime: 0s
    min_success_rate: 0
  session:
    ttl: 10m

local_server:
  enabled: true
  listen: ""
  auth:
    username: $initialUsername
    password: $initialPassword
  shared_revision: 1
  credential_generation: 2

management:
  enabled: true
  listen: 0.0.0.0:29888
  probe_target: http://direct:8080/health
  health_check_interval: 1h
  password: $initialPassword

subscription_refresh:
  enabled: false
  interval: 1h
  timeout: 10s
  health_check_timeout: 20s
  drain_timeout: 5s
  min_available_nodes: 0

source_sync:
  enabled: false
  manifest_url: ""
  manifest_token: ""
  refresh_interval: 1h
  request_timeout: 10s
  fallback_subscriptions: []

connectors: []
subscriptions: []
nodes: []
"@

New-Item -ItemType Directory -Force -Path $configDir | Out-Null
Write-Utf8NoBom -Path $configPath -Value $configYaml

$metadata = [ordered]@{
    validationId = $ValidationId
    image = $Image
    artifactDir = $artifactDir
    configPath = $configPath
    network = $networkName
    volume = $volumeName
    containers = @($originName, $upstreamName, $serviceName, $clientName)
    ports = [ordered]@{
        management = $managementPort
        proxy = $proxyPort
        origin = $originPort
        upstreamCounter = $upstreamCounterPort
    }
    legacyBefore = $legacyBefore
}
Write-Utf8NoBom -Path $metadataPath -Value ($metadata | ConvertTo-Json -Depth 100)

try {
    $null = Invoke-Docker -DockerArgs @("network", "create", "--label", $label, $networkName)
    $null = Invoke-Docker -DockerArgs @("volume", "create", "--label", $label, $volumeName)

    $fixtureMount = "type=bind,source=$fixturePath,target=/fixture.py,readonly"
    $configMount = "type=bind,source=$configDir,target=/var/lib/easy-proxy/config"
    $dataMount = "type=volume,source=$volumeName,target=/var/lib/easy-proxy/data"

    $null = Invoke-Docker -DockerArgs @(
        "run", "-d", "--name", $originName, "--label", $label,
        "--network", $networkName, "--network-alias", "direct",
        "-p", "${originPort}:8080", "--mount", $fixtureMount,
        "python:3.12-slim", "python", "/fixture.py", "origin", "--listen", "0.0.0.0:8080", "--name", "direct"
    )
    $null = Invoke-Docker -DockerArgs @(
        "run", "-d", "--name", $upstreamName, "--label", $label,
        "--network", $networkName, "--network-alias", "counted-proxy",
        "-p", "${upstreamCounterPort}:8081", "--mount", $fixtureMount,
        "python:3.12-slim", "python", "/fixture.py", "proxy", "--listen", "0.0.0.0:3128", "--counter-listen", "0.0.0.0:8081"
    )
    $null = Invoke-Docker -DockerArgs @(
        "run", "-d", "--name", $serviceName, "--label", $label,
        "--network", $networkName, "--network-alias", "easyproxy",
        "-p", "${managementPort}:29888", "-p", "${proxyPort}:22323",
        "--mount", $configMount, "--mount", $dataMount,
        "-e", "EASY_PROXY_CONFIG_PATH=/var/lib/easy-proxy/config/config.yaml",
        $Image
    )
    $null = Invoke-Docker -DockerArgs @(
        "run", "-d", "--name", $clientName, "--label", $label,
        "--network", $networkName, "--entrypoint", "sh",
        "curlimages/curl:8.12.1", "-c", "sleep 86400"
    )

    $authHeaders = New-BasicHeaders -Username $initialUsername -Password $initialPassword
    $status = Wait-ForLocalServer -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders
    $statusJson = $status | ConvertTo-Json -Depth 20 -Compress
    $configView = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/config" -Headers $authHeaders).Body
    Assert-Check -Checks $checks -Name "01_startup_ready_and_redacted" -Condition (
        $status.enabled -and $status.dispatcher_ready -and $configView.password_set -and
        -not $statusJson.Contains($initialPassword) -and -not ($configView.PSObject.Properties.Name -contains "auth_password")
    ) -Details "enabled=$($status.enabled) ready=$($status.dispatcher_ready) listen=$($status.listen)"

    $login = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/auth" -Body @{ username = $initialUsername; password = $initialPassword }
    $wrongUser = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/auth" -Body @{ username = "wrong"; password = $initialPassword } -AllowFailure
    $wrongPassword = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/auth" -Body @{ username = $initialUsername; password = "wrong" } -AllowFailure
    $oldSessionToken = [string]$login.Body.token
    Assert-Check -Checks $checks -Name "02_canonical_login_contract" -Condition (
        -not [string]::IsNullOrWhiteSpace($oldSessionToken) -and $wrongUser.StatusCode -eq 401 -and $wrongPassword.StatusCode -eq 401
    ) -Details "wrongUser=$($wrongUser.StatusCode) wrongPassword=$($wrongPassword.StatusCode)"

    $shared = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/profiles/shared" -Headers $authHeaders).Body
    $shared = (Set-SharedProfileEnabled -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -Resource $shared -Enabled $false).Body.resource
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $directZero = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-a" -Password $initialPassword -Protocol HTTP -Path "zero-direct"
    $originAfterDirect = Get-CounterSnapshot -Uri $originCounterUrl
    $upstreamAfterDirect = Get-CounterSnapshot -Uri $upstreamCounterUrl
    $shared = (Set-SharedProfileEnabled -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -Resource $shared -Enabled $true).Body.resource
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $proxyZero = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-a" -Password $initialPassword -Protocol HTTP -Path "zero-proxy"
    $originAfterProxy = Get-CounterSnapshot -Uri $originCounterUrl
    $upstreamAfterProxy = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "03_zero_node_direct_and_proxy_failure" -Condition (
        $directZero.StatusCode -eq 200 -and (Get-CounterTotal $originAfterDirect) -eq 1 -and
        (Get-CounterTotal $upstreamAfterDirect) -eq 0 -and $proxyZero.StatusCode -eq 502 -and
        (Get-CounterTotal $originAfterProxy) -eq 0 -and (Get-CounterTotal $upstreamAfterProxy) -eq 0
    ) -Details "direct=$($directZero.StatusCode) proxy=$($proxyZero.StatusCode)"

    $null = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/nodes/config" -Headers $authHeaders -Body @{
        name = "counted-upstream"
        uri = "http://counted-proxy:3128"
    }
    $null = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/reload" -Headers $authHeaders
    $null = Wait-ForAvailableNode -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl

    $shared = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/profiles/shared" -Headers $authHeaders).Body
    if ($shared.profile.enabled) {
        $shared = (Set-SharedProfileEnabled -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -Resource $shared -Enabled $false).Body.resource
    }
    $sharedOffRequest = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-a" -Password $initialPassword -Protocol HTTP -Path "shared-off"
    $originSharedOff = Get-CounterSnapshot -Uri $originCounterUrl
    $upstreamSharedOff = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "04_shared_off_is_direct" -Condition (
        $sharedOffRequest.StatusCode -eq 200 -and (Get-CounterTotal $originSharedOff) -eq 1 -and (Get-CounterTotal $upstreamSharedOff) -eq 0
    ) -Details "status=$($sharedOffRequest.StatusCode)"

    $deviceBCreate = Put-DeviceProfile -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -DeviceId "device-b" -Profile (New-ForwardingProfile -Enabled $true)
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $deviceBProxy = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol HTTP -Path "device-b-proxy"
    $upstreamDeviceB = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "05_independent_on_uses_upstream" -Condition (
        $deviceBProxy.StatusCode -eq 200 -and (Get-CounterTotal $upstreamDeviceB) -eq 1
    ) -Details "status=$($deviceBProxy.StatusCode) revision=$($deviceBCreate.Body.revision)"

    $shared = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/profiles/shared" -Headers $authHeaders).Body
    $shared = (Set-SharedProfileEnabled -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -Resource $shared -Enabled $true).Body.resource
    $null = Put-DeviceProfile -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -DeviceId "device-a" -Profile (New-ForwardingProfile -Enabled $false)
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $deviceADirect = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-a" -Password $initialPassword -Protocol HTTP -Path "device-a-direct"
    $originDeviceA = Get-CounterSnapshot -Uri $originCounterUrl
    $upstreamDeviceA = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "06_independent_off_is_direct" -Condition (
        $deviceADirect.StatusCode -eq 200 -and (Get-CounterTotal $originDeviceA) -eq 1 -and (Get-CounterTotal $upstreamDeviceA) -eq 0
    ) -Details "status=$($deviceADirect.StatusCode)"

    $deviceBBefore = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/devices/device-b" -Headers $authHeaders).Body
    $deviceBBeforeJson = $deviceBBefore.profile.profile | ConvertTo-Json -Depth 100 -Compress
    $shared = (Set-SharedProfileEnabled -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -Resource $shared -Enabled $false).Body.resource
    $deviceBAfter = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/devices/device-b" -Headers $authHeaders).Body
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $deviceBAfterSharedChange = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol HTTP -Path "device-b-after-shared"
    $upstreamAfterSharedChange = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "07_shared_change_does_not_mutate_independent" -Condition (
        $deviceBBefore.profile.revision -eq $deviceBAfter.profile.revision -and
        $deviceBBeforeJson -eq ($deviceBAfter.profile.profile | ConvertTo-Json -Depth 100 -Compress) -and
        $deviceBAfterSharedChange.StatusCode -eq 200 -and (Get-CounterTotal $upstreamAfterSharedChange) -eq 1
    ) -Details "beforeRevision=$($deviceBBefore.profile.revision) afterRevision=$($deviceBAfter.profile.revision)"

    $deleteHeaders = @{} + $authHeaders
    $deleteHeaders["If-Match"] = '"' + [string]$deviceBAfter.profile.revision + '"'
    $null = Invoke-JsonApi -Method DELETE -Uri "$managementBaseUrl/api/local-server/devices/device-b/profile" -Headers $deleteHeaders -Body @{
        expected_revision = [int64]$deviceBAfter.profile.revision
    }
    $deviceBShared = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/devices/device-b" -Headers $authHeaders).Body
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $deviceBReturned = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol HTTP -Path "device-b-returned"
    $upstreamReturned = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "08_delete_independent_returns_to_shared" -Condition (
        $deviceBShared.profile_mode -eq "shared" -and $deviceBReturned.StatusCode -eq 200 -and (Get-CounterTotal $upstreamReturned) -eq 0
    ) -Details "mode=$($deviceBShared.profile_mode)"

    $null = Put-DeviceProfile -ManagementBaseUrl $managementBaseUrl -Headers $authHeaders -DeviceId "device-b" -Profile (New-ForwardingProfile -Enabled $true)
    $deviceBSummary = @((Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/devices" -Headers $authHeaders).Body.devices |
        Where-Object { $_.device_id -eq "device-b" } | Select-Object -First 1)
    if ($deviceBSummary.Count -ne 1 -or [string]::IsNullOrWhiteSpace([string]$deviceBSummary[0].last_seen_ip)) {
        throw "device-b did not expose a usable last_seen_ip for mapping validation"
    }
    $mappingAddress = [string]$deviceBSummary[0].last_seen_ip
    $mappingCIDR = if ($mappingAddress.Contains(":")) { "$mappingAddress/128" } else { "$mappingAddress/32" }
    $mappingHeaders = @{} + $authHeaders
    $mappingHeaders["If-None-Match"] = "*"
    $null = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/local-server/ip-mappings" -Headers $mappingHeaders -Body @{
        expected_revision = 0
        cidr = $mappingCIDR
        device_id = "device-b"
        priority = 100
        enabled = $true
    }
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $mappedB = Invoke-ProxyRequest -ClientName $clientName -DeviceId "" -Password $initialPassword -Protocol HTTP -Path "mapped-b"
    $mappedBUpstream = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $explicitA = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-a" -Password $initialPassword -Protocol HTTP -Path "explicit-a"
    $explicitAUpstream = Get-CounterSnapshot -Uri $upstreamCounterUrl
    Assert-Check -Checks $checks -Name "09_explicit_device_overrides_ip_mapping" -Condition (
        $mappedB.StatusCode -eq 200 -and (Get-CounterTotal $mappedBUpstream) -eq 1 -and
        $explicitA.StatusCode -eq 200 -and (Get-CounterTotal $explicitAUpstream) -eq 0
    ) -Details "mapped=$($mappedB.StatusCode)/$(Get-CounterTotal $mappedBUpstream) explicit=$($explicitA.StatusCode)/$(Get-CounterTotal $explicitAUpstream)"

    Reset-Counter -Uri $originResetUrl
    Reset-Counter -Uri $upstreamResetUrl
    $httpResult = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol HTTP -Path "protocol-http"
    $httpCount = Get-CounterTotal (Get-CounterSnapshot -Uri $upstreamCounterUrl)
    $connectResult = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol CONNECT -Path "protocol-connect"
    $connectCount = Get-CounterTotal (Get-CounterSnapshot -Uri $upstreamCounterUrl)
    $socksResult = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol SOCKS5 -Path "protocol-socks"
    $socksCount = Get-CounterTotal (Get-CounterSnapshot -Uri $upstreamCounterUrl)
    Assert-Check -Checks $checks -Name "10_protocols_share_device_profile" -Condition (
        $httpResult.StatusCode -eq 200 -and $connectResult.StatusCode -eq 200 -and $socksResult.StatusCode -eq 200 -and
        $httpCount -ge 1 -and $connectCount -ge 2 -and $socksCount -ge 3
    ) -Details "http=$($httpResult.StatusCode) connect=$($connectResult.StatusCode) socks=$($socksResult.StatusCode) counts=$httpCount/$connectCount/$socksCount"

    $sharedForConflict = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/profiles/shared" -Headers $authHeaders).Body
    $profileForConflict = Copy-JsonObject -Value $sharedForConflict.profile
    $profileForConflict.enabled = -not [bool]$profileForConflict.enabled
    $conflictHeaders = @{} + $authHeaders
    $conflictHeaders["If-Match"] = '"' + [string]$sharedForConflict.revision + '"'
    $updatedShared = Invoke-JsonApi -Method PUT -Uri "$managementBaseUrl/api/local-server/profiles/shared" -Headers $conflictHeaders -Body @{
        expected_revision = [int64]$sharedForConflict.revision
        profile = $profileForConflict
    }
    $stale = Invoke-JsonApi -Method PUT -Uri "$managementBaseUrl/api/local-server/profiles/shared" -Headers $conflictHeaders -Body @{
        expected_revision = [int64]$sharedForConflict.revision
        profile = $profileForConflict
    } -AllowFailure
    Assert-Check -Checks $checks -Name "11_stale_revision_conflict" -Condition (
        $stale.StatusCode -eq 409 -and [int64]$stale.Body.current_revision -eq [int64]$updatedShared.Body.revision
    ) -Details "status=$($stale.StatusCode) current=$($stale.Body.current_revision)"

    $statusBeforeRotation = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/status" -Headers $authHeaders).Body
    $rotation = Invoke-JsonApi -Method PUT -Uri "$managementBaseUrl/api/local-server/config" -Headers $authHeaders -Body @{
        auth_username = $initialUsername
        auth_password = $rotatedPassword
    }
    $rotatedHeaders = New-BasicHeaders -Username $initialUsername -Password $rotatedPassword
    $statusAfterRotation = (Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/status" -Headers $rotatedHeaders).Body
    $oldSession = Invoke-JsonApi -Method GET -Uri "$managementBaseUrl/api/local-server/status" -Headers @{ Authorization = "Bearer $oldSessionToken" } -AllowFailure
    $newLogin = Invoke-JsonApi -Method POST -Uri "$managementBaseUrl/api/auth" -Body @{ username = $initialUsername; password = $rotatedPassword }
    $oldProxyCredential = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $initialPassword -Protocol HTTP -Path "old-credential"
    $newProxyCredential = Invoke-ProxyRequest -ClientName $clientName -DeviceId "device-b" -Password $rotatedPassword -Protocol HTTP -Path "new-credential"
    Assert-Check -Checks $checks -Name "12_credential_rotation_is_hot" -Condition (
        -not $rotation.Body.need_reload -and $statusBeforeRotation.listen -eq $statusAfterRotation.listen -and
        [uint64]$statusAfterRotation.credential_generation -gt [uint64]$statusBeforeRotation.credential_generation -and
        $oldSession.StatusCode -eq 401 -and -not [string]::IsNullOrWhiteSpace([string]$newLogin.Body.token) -and
        $oldProxyCredential.StatusCode -eq 407 -and $newProxyCredential.StatusCode -eq 200
    ) -Details "listen=$($statusAfterRotation.listen) generation=$($statusBeforeRotation.credential_generation)->$($statusAfterRotation.credential_generation)"

    $index = Invoke-WebRequest -UseBasicParsing -Uri "$managementBaseUrl/" -TimeoutSec 30
    $assetMatches = [regex]::Matches($index.Content, '(?:src|href)="(?<path>/assets/[^"]+)"')
    $assetsOk = $assetMatches.Count -gt 0
    foreach ($match in $assetMatches) {
        $assetResponse = Invoke-WebRequest -UseBasicParsing -Uri ($managementBaseUrl + $match.Groups["path"].Value) -TimeoutSec 30
        if ($assetResponse.StatusCode -ne 200 -or $assetResponse.Headers["Content-Type"] -like "text/html*") {
            $assetsOk = $false
        }
    }
    $spa = Invoke-WebRequest -UseBasicParsing -Uri "$managementBaseUrl/devices/unknown-route" -TimeoutSec 30
    Assert-Check -Checks $checks -Name "13_embedded_assets_available" -Condition (
        $index.StatusCode -eq 200 -and $spa.StatusCode -eq 200 -and $assetsOk
    ) -Details "assets=$($assetMatches.Count)"
}
catch {
    $failure = $_
    Write-Host "[local-server-e2e] failure: $($_.Exception.Message)" -ForegroundColor Red
    foreach ($name in @($serviceName, $originName, $upstreamName)) {
        try {
            $logs = @(& docker logs $name 2>&1)
            if ($logs.Count -gt 0) {
                Write-Utf8NoBom -Path (Join-Path $artifactDir "$name.log") -Value ($logs -join [Environment]::NewLine)
            }
        }
        catch {
        }
    }
}
finally {
    try {
        if (-not $KeepRuntime) {
            Remove-DisposableTopology -ValidationId $ValidationId -Metadata $metadata
        }
        $legacyAfter = Get-LegacyContainerInvariant
        Assert-LegacyContainerInvariant -Before $legacyBefore -After $legacyAfter
        $checks["14_legacy_container_unchanged"] = [ordered]@{
            passed = $true
            details = "legacy container identity/state unchanged"
        }
    }
    catch {
        $checks["14_legacy_container_unchanged"] = [ordered]@{
            passed = $false
            details = $_.Exception.Message
        }
        if ($null -eq $failure) {
            $failure = $_
        }
    }

    $evidence = [ordered]@{
        validationId = $ValidationId
        verifiedAt = (Get-Date).ToString("o")
        image = $Image
        imageId = [string](@(& docker image inspect $Image --format "{{.Id}}" 2>$null) | Select-Object -First 1)
        managementBaseUrl = $managementBaseUrl
        proxyPort = $proxyPort
        topology = $metadata
        checks = $checks
        legacyAfter = $legacyAfter
        keptRuntime = [bool]$KeepRuntime
        success = ($null -eq $failure)
        error = if ($null -eq $failure) { "" } else { $failure.Exception.Message }
    }
    Write-Utf8NoBom -Path $evidencePath -Value ($evidence | ConvertTo-Json -Depth 100)
}

if ($null -ne $failure) {
    throw $failure
}

Write-Host "[local-server-e2e] success"
Write-Host "[local-server-e2e] evidence: $evidencePath"
if ($KeepRuntime) {
    Write-Host "[local-server-e2e] runtime retained; cleanup with -ValidationId $ValidationId -CleanupOnly"
}

if ((-not $KeepArtifacts) -and (-not $KeepRuntime)) {
    $resolvedArtifactRoot = [System.IO.Path]::GetFullPath($artifactRoot).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    $resolvedArtifactDir = [System.IO.Path]::GetFullPath($artifactDir)
    if (-not $resolvedArtifactDir.StartsWith($resolvedArtifactRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "refusing to remove artifact path outside validation root: $resolvedArtifactDir"
    }
    Remove-Item -LiteralPath $resolvedArtifactDir -Recurse -Force
}
