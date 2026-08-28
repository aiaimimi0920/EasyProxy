[CmdletBinding()]
param(
    [string]$ValidationId = ("runtime-" + (Get-Date -Format "yyyyMMdd-HHmmss")),
    [string]$Image = "",
    [string]$ConfigPath = "",
    [string]$TopologyPath = "",
    [int]$ScenarioTimeoutSeconds = 720,
    [switch]$KeepArtifacts,
    [switch]$SkipCleanup,
    [string]$DockerNetworkName = "EasyAiMi"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RepoRoot {
    return (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path
}

. (Join-Path (Get-RepoRoot) "scripts\lib\easyproxy-common.ps1")
. (Join-Path (Get-RepoRoot) "scripts\lib\easyproxy-runtime-config.ps1")
. (Join-Path (Get-RepoRoot) "scripts\lib\easyproxy-topology.ps1")

function Invoke-Audit {
    param(
        [Parameter(Mandatory = $true)][string]$ScenarioName,
        [string[]]$Subscriptions = @(),
        [string[]]$ProxyUris = @(),
        [string[]]$FallbackSubscriptions = @(),
        [string[]]$DnsServers = @(),
        [string]$ManifestUrl = "",
        [string]$ManifestToken = "",
        [string]$ConnectorsJson = "",
        [switch]$RequireManifestHealthy,
        [switch]$RequireFallbackActive,
        [int]$RequireConnectorInstanceCount = 0,
        [int]$RequireStableNodeProxies = 1
    )

    $scenarioDir = Join-Path $artifactDir $ScenarioName
    New-Item -ItemType Directory -Force -Path $scenarioDir | Out-Null
    $summaryPath = Join-Path $scenarioDir "summary.json"

    $args = @(
        (Join-Path $repoRoot "scripts\easyproxy_source_audit.py"),
        "--audit-id", ("{0}-{1}" -f $ValidationId, $ScenarioName),
        "--image", $effectiveImage,
        "--build-if-missing",
        "--output-path", $summaryPath,
        "--artifact-dir", $scenarioDir,
        "--scenario-timeout-seconds", $ScenarioTimeoutSeconds,
        "--docker-network-name", $DockerNetworkName,
        "--require-stable-node-proxies", $RequireStableNodeProxies
    )

    foreach ($sub in @($Subscriptions | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
        $args += @("--subscription", $sub)
    }
    foreach ($uri in @($ProxyUris | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
        $args += @("--proxy-uri", $uri)
    }
    foreach ($sub in @($FallbackSubscriptions | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
        $args += @("--fallback-subscription", $sub)
    }
    foreach ($dnsServer in @($DnsServers | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
        $args += @("--dns-server", $dnsServer)
    }
    if (-not [string]::IsNullOrWhiteSpace($ManifestUrl)) {
        $args += @("--manifest-url", $ManifestUrl)
    }
    if ($RequireManifestHealthy) {
        $args += "--require-manifest-healthy"
    }
    if ($RequireFallbackActive) {
        $args += "--require-fallback-active"
    }
    if ($RequireConnectorInstanceCount -gt 0) {
        $args += @("--require-connector-instance-count", $RequireConnectorInstanceCount)
    }
    if ($KeepArtifacts) {
        $args += "--keep-artifacts"
    }
    if ($SkipCleanup) {
        $args += "--skip-cleanup"
    }

    Write-Host "[runtime:$ScenarioName] auditing..."
    $previousManifestToken = [Environment]::GetEnvironmentVariable('EASYPROXY_AUDIT_MANIFEST_TOKEN', 'Process')
    $previousConnectorsJson = [Environment]::GetEnvironmentVariable('EASYPROXY_AUDIT_CONNECTORS_JSON', 'Process')
    try {
        [Environment]::SetEnvironmentVariable('EASYPROXY_AUDIT_MANIFEST_TOKEN', $ManifestToken, 'Process')
        [Environment]::SetEnvironmentVariable('EASYPROXY_AUDIT_CONNECTORS_JSON', $ConnectorsJson, 'Process')
        & python @args | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "runtime audit failed for $ScenarioName"
        }
    }
    finally {
        [Environment]::SetEnvironmentVariable('EASYPROXY_AUDIT_MANIFEST_TOKEN', $previousManifestToken, 'Process')
        [Environment]::SetEnvironmentVariable('EASYPROXY_AUDIT_CONNECTORS_JSON', $previousConnectorsJson, 'Process')
    }

    try {
        $payload = Get-Content -Path $summaryPath -Raw | ConvertFrom-Json
    }
    finally {
        Remove-Item -LiteralPath $summaryPath -Force -ErrorAction SilentlyContinue
    }
    $script:summary += [pscustomobject]@{
        scenario        = $ScenarioName
        audit_id        = [string]$payload.audit_id
        validated_image = [string]$payload.validated_image
        inputs          = [pscustomobject]@{
            subscription_count          = @($payload.inputs.subscriptions).Count
            proxy_uri_count             = @($payload.inputs.proxy_uris).Count
            fallback_subscription_count = @($payload.inputs.fallback_subscriptions).Count
            connector_count             = [int]$payload.inputs.connector_count
            manifest_enabled            = -not [string]::IsNullOrWhiteSpace([string]$payload.inputs.manifest_url)
        }
        nodes           = [pscustomobject]@{
            total_nodes            = [int]$payload.nodes.total_nodes
            available_nodes        = [int]$payload.nodes.available_nodes
            stable_available_count = @($payload.nodes.stable_available_uris).Count
        }
    }
    Write-Host "[runtime:$ScenarioName] passed"
    return $payload
}

function Get-StableAvailableUris {
    param(
        [Parameter(Mandatory = $true)]$Payload
    )

    if ($null -eq $Payload) {
        return @()
    }

    $nodesProperty = $Payload.PSObject.Properties['nodes']
    if ($null -eq $nodesProperty -or $null -eq $Payload.nodes) {
        return @()
    }

    $stableUrisProperty = $Payload.nodes.PSObject.Properties['stable_available_uris']
    if ($null -eq $stableUrisProperty) {
        return @()
    }

    return @($Payload.nodes.stable_available_uris | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
}

$repoRoot = Get-RepoRoot
$artifactDir = Join-Path $repoRoot ("tmp\easy-proxy-runtime-validation\" + $ValidationId)
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null

$effectiveConfigPath = $ConfigPath
if ([string]::IsNullOrWhiteSpace($effectiveConfigPath)) {
    $effectiveConfigPath = Join-Path $repoRoot "deploy\service\base\config.yaml"
}

$effectiveImage = $Image
if ([string]::IsNullOrWhiteSpace($effectiveImage)) {
    $effectiveImage = "easyproxy/runtime-validation:$ValidationId"
}

$serviceRuntime = Read-EasyProxyRuntimeConfig -ConfigPath $effectiveConfigPath
$configuredDnsServers = @('1.1.1.1', '8.8.8.8')
$sourceSyncConfig = Get-EasyProxyRuntimeSection -Object $serviceRuntime -Name 'source_sync'
$configuredLocalSubscriptions = @(Get-EasyProxyRuntimeValue -Object $serviceRuntime -Name 'subscriptions' -Default @())

if ($configuredLocalSubscriptions.Count -lt 1) {
    throw "runtime config does not define subscriptions for local subscription validation"
}

$misubPublicUrl = ''
$misubConnectorProfileId = 'easyproxies-ech-runtime'
$manifestToken = [string](Get-EasyProxyRuntimeValue -Object $sourceSyncConfig -Name 'manifest_token' -Default '')
$topology = $null
if ([string]::IsNullOrWhiteSpace($TopologyPath)) {
    $candidate = Join-Path $repoRoot 'topology.yaml'
    if (Test-Path -LiteralPath $candidate) { $TopologyPath = $candidate }
}
if (-not [string]::IsNullOrWhiteSpace($TopologyPath)) {
    $topology = Read-EasyProxyTopology -TopologyPath $TopologyPath
    $names = Get-EasyProxyResourceNames -TopologyPath $TopologyPath
    if ([bool]$topology.cloudflare.use_pages_dev) {
        $misubPublicUrl = "https://$($names.pages_project).pages.dev"
    }
    $misubConnectorProfileId = [string]$topology.misub.default_profile
    if ([string]::IsNullOrWhiteSpace($manifestToken)) {
        $manifestToken = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.misub_manifest_token) -Purpose 'runtime validation' -Optional
    }
}
if ([string]::IsNullOrWhiteSpace($misubPublicUrl)) {
    $manifestUrl = [string](Get-EasyProxyRuntimeValue -Object $sourceSyncConfig -Name 'manifest_url' -Default '')
    if ($manifestUrl -match '^(https?://[^/]+)') { $misubPublicUrl = $Matches[1] }
}
if ([string]::IsNullOrWhiteSpace($manifestToken)) {
    throw "Unable to resolve MiSub manifest token from runtime config or topology environment reference"
}

$workerUrl = ''
$workerAccessToken = ''
$connectors = Get-EasyProxyRuntimeValue -Object $serviceRuntime -Name 'connectors' -Default @()
if ($connectors -and @($connectors).Count -gt 0) {
    $firstConnector = @($connectors)[0]
    $connectorConfig = Get-EasyProxyRuntimeSection -Object $firstConnector -Name 'connector_config'
    $workerAccessToken = [string](Get-EasyProxyRuntimeValue -Object $connectorConfig -Name 'access_token' -Default '')
    $workerUrl = [string](Get-EasyProxyRuntimeValue -Object $firstConnector -Name 'input' -Default '')
}
if ([string]::IsNullOrWhiteSpace($workerAccessToken) -and $null -ne $topology) {
    $workerAccessToken = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.ech_token) -Purpose 'runtime validation' -Optional
}
if ([string]::IsNullOrWhiteSpace($workerUrl)) {
    throw 'Unable to resolve ECH worker URL from runtime connectors.'
}
if ([string]::IsNullOrWhiteSpace($misubPublicUrl)) {
    throw 'Unable to resolve MiSub public URL from topology or runtime manifest_url.'
}
if ([string]::IsNullOrWhiteSpace($workerAccessToken)) {
    throw "Unable to resolve ECH worker access token from runtime config or topology environment reference"
}

$summary = @()
$connectorPayload = ConvertTo-Json -InputObject @(
    @{
        name = "ECH Local Preferred"
        input = $workerUrl
        enabled = $true
        template_only = $false
        connector_type = "ech_worker"
        connector_config = @{
            local_protocol = "socks5"
            access_token   = $workerAccessToken
        }
    }
) -Depth 20 -Compress

$localSubscription = Invoke-Audit -ScenarioName "local-subscription" -Subscriptions $configuredLocalSubscriptions -DnsServers $configuredDnsServers
$manifestSubscription = Invoke-Audit `
    -ScenarioName "manifest-subscription" `
    -DnsServers $configuredDnsServers `
    -ManifestUrl "$misubPublicUrl/api/manifest/aggregator-global" `
    -ManifestToken $manifestToken `
    -RequireManifestHealthy

$directProxyCandidates = @()
foreach ($payload in @($localSubscription, $manifestSubscription)) {
    foreach ($uri in @(Get-StableAvailableUris -Payload $payload)) {
        $candidate = [string]$uri
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and -not ($directProxyCandidates -contains $candidate)) {
            $directProxyCandidates += $candidate
        }
    }
}
if ($directProxyCandidates.Count -lt 1) {
    throw "No reusable stable direct proxy URI was discovered during subscription validation"
}

$directValidated = $false
$directErrors = @()
$candidateIndex = 0
foreach ($candidate in $directProxyCandidates) {
    $candidateIndex++
    try {
        $null = Invoke-Audit -ScenarioName "local-direct-proxy" -ProxyUris @($candidate) -DnsServers $configuredDnsServers
        $directValidated = $true
        break
    }
    catch {
        $directErrors += "candidate #$candidateIndex => $($_.Exception.Message)"
    }
}
if (-not $directValidated) {
    throw "local-direct-proxy failed for all candidate URIs: $($directErrors -join '; ')"
}

$fallbackSubscriptions = @(Get-EasyProxyRuntimeValue -Object $sourceSyncConfig -Name 'fallback_subscriptions' -Default @())
if ($fallbackSubscriptions.Count -lt 1) {
    throw "Runtime config must define source_sync.fallback_subscriptions; no maintainer-owned fallback is assumed."
}
$null = Invoke-Audit `
    -ScenarioName "fallback-subscription" `
    -DnsServers $configuredDnsServers `
    -ManifestUrl "http://127.0.0.1:1/api/manifest/broken" `
    -ManifestToken $manifestToken `
    -FallbackSubscriptions $fallbackSubscriptions `
    -RequireFallbackActive

$null = Invoke-Audit `
    -ScenarioName "local-connector" `
    -DnsServers $configuredDnsServers `
    -ConnectorsJson $connectorPayload `
    -RequireConnectorInstanceCount 5

$null = Invoke-Audit `
    -ScenarioName "manifest-connector" `
    -DnsServers $configuredDnsServers `
    -ManifestUrl "$misubPublicUrl/api/manifest/$misubConnectorProfileId" `
    -ManifestToken $manifestToken `
    -RequireManifestHealthy `
    -RequireConnectorInstanceCount 5

$summaryPath = Join-Path $artifactDir "summary.json"
$summary | ConvertTo-Json -Depth 100 | Set-Content -Path $summaryPath -Encoding UTF8

Write-Host "[runtime] success"
Write-Host "[runtime] summary: $summaryPath"
Write-Host "[runtime] artifacts retained at $artifactDir"
