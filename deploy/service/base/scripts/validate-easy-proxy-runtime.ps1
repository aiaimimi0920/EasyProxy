[CmdletBinding()]
param(
    [string]$ValidationId = ("runtime-" + (Get-Date -Format "yyyyMMdd-HHmmss")),
    [string]$Image = "",
    [string]$ConfigPath = "",
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
. (Join-Path (Get-RepoRoot) "scripts\lib\easyproxy-config.ps1")

function Invoke-Audit {
    param(
        [Parameter(Mandatory = $true)][string]$ScenarioName,
        [string[]]$Subscriptions = @(),
        [string[]]$ProxyUris = @(),
        [string[]]$FallbackSubscriptions = @(),
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
    if (-not [string]::IsNullOrWhiteSpace($ManifestUrl)) {
        $args += @("--manifest-url", $ManifestUrl)
    }
    if (-not [string]::IsNullOrWhiteSpace($ManifestToken)) {
        $args += @("--manifest-token", $ManifestToken)
    }
    if (-not [string]::IsNullOrWhiteSpace($ConnectorsJson)) {
        $args += @("--connectors-json", $ConnectorsJson)
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
    & python @args | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "runtime audit failed for $ScenarioName"
    }

    $payload = Get-Content -Path $summaryPath -Raw | ConvertFrom-Json
    $script:summary += $payload
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
    $effectiveConfigPath = Join-Path $repoRoot "config.yaml"
}

$effectiveImage = $Image
if ([string]::IsNullOrWhiteSpace($effectiveImage)) {
    $effectiveImage = "easyproxy/runtime-validation:$ValidationId"
}

$rootConfig = Read-EasyProxyConfig -ConfigPath $effectiveConfigPath
$serviceBase = Get-EasyProxyConfigSection -Config $rootConfig -Name 'serviceBase'
$serviceRuntime = Get-EasyProxyConfigSection -Config $serviceBase -Name 'runtime'
$sourceSyncConfig = Get-EasyProxyConfigSection -Config $serviceRuntime -Name 'source_sync'
$misub = Get-EasyProxyConfigSection -Config $rootConfig -Name 'misub'
$misubPages = Get-EasyProxyConfigSection -Config $misub -Name 'pages'
$misubDocker = Get-EasyProxyConfigSection -Config $misub -Name 'docker'
$misubDockerEnv = Get-EasyProxyConfigSection -Config $misubDocker -Name 'env'
$echCloudflare = Get-EasyProxyConfigSection -Config $rootConfig -Name 'echWorkersCloudflare'
$echSecrets = Get-EasyProxyConfigSection -Config $echCloudflare -Name 'secrets'
$configuredLocalSubscriptions = @(Get-EasyProxyConfigValue -Object $serviceRuntime -Name 'subscriptions' -Default @())

if ($configuredLocalSubscriptions.Count -lt 1) {
    throw "config.yaml does not define any serviceBase.runtime.subscriptions for local subscription validation"
}

$misubPublicUrl = [string](Get-EasyProxyConfigValue -Object $misubPages -Name 'publicUrl' -Default 'https://misub.aiaimimi.com')
$misubConnectorProfileId = [string](Get-EasyProxyConfigValue -Object $misubPages -Name 'connectorProfileId' -Default 'easyproxies-ech-runtime')
$manifestToken = [string](Get-EasyProxyConfigValue -Object $sourceSyncConfig -Name 'manifest_token' -Default '')
if ([string]::IsNullOrWhiteSpace($manifestToken)) {
    $manifestToken = [string](Get-EasyProxyConfigValue -Object $misubDockerEnv -Name 'MANIFEST_TOKEN' -Default '')
}
if ([string]::IsNullOrWhiteSpace($manifestToken)) {
    throw "Unable to resolve MiSub manifest token from config.yaml"
}

$workerBaseUrl = [string](Get-EasyProxyConfigValue -Object $echCloudflare -Name 'publicUrl' -Default 'https://proxyservice-ech-workers.aiaimimi.com')
$workerUrl = "{0}:443" -f $workerBaseUrl
$workerAccessToken = [string](Get-EasyProxyConfigValue -Object $echSecrets -Name 'ECH_TOKEN' -Default '')
if ([string]::IsNullOrWhiteSpace($workerAccessToken)) {
    $connectors = Get-EasyProxyConfigValue -Object $serviceRuntime -Name 'connectors' -Default @()
    if ($connectors -and @($connectors).Count -gt 0) {
        $firstConnector = @($connectors)[0]
        $connectorConfig = Get-EasyProxyConfigSection -Config $firstConnector -Name 'connector_config'
        $workerAccessToken = [string](Get-EasyProxyConfigValue -Object $connectorConfig -Name 'access_token' -Default '')
        $inputUrl = [string](Get-EasyProxyConfigValue -Object $firstConnector -Name 'input' -Default '')
        if (-not [string]::IsNullOrWhiteSpace($inputUrl)) {
            $workerUrl = $inputUrl
        }
    }
}
if ([string]::IsNullOrWhiteSpace($workerAccessToken)) {
    throw "Unable to resolve ECH worker access token from config.yaml"
}

$summary = @()
$connectorPayload = @(
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
) | ConvertTo-Json -Depth 20 -Compress

$localSubscription = Invoke-Audit -ScenarioName "local-subscription" -Subscriptions $configuredLocalSubscriptions
$manifestSubscription = Invoke-Audit `
    -ScenarioName "manifest-subscription" `
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
foreach ($candidate in $directProxyCandidates) {
    try {
        $null = Invoke-Audit -ScenarioName "local-direct-proxy" -ProxyUris @($candidate)
        $directValidated = $true
        break
    }
    catch {
        $directErrors += "$candidate => $($_.Exception.Message)"
    }
}
if (-not $directValidated) {
    throw "local-direct-proxy failed for all candidate URIs: $($directErrors -join '; ')"
}

$fallbackSubscriptions = @(Get-EasyProxyConfigValue -Object $sourceSyncConfig -Name 'fallback_subscriptions' -Default @())
if ($fallbackSubscriptions.Count -lt 1) {
    $fallbackSubscriptions = @("https://sub.aiaimimi.com/subs/clash.yaml")
}
$null = Invoke-Audit `
    -ScenarioName "fallback-subscription" `
    -ManifestUrl "http://127.0.0.1:1/api/manifest/broken" `
    -ManifestToken $manifestToken `
    -FallbackSubscriptions $fallbackSubscriptions `
    -RequireFallbackActive

$null = Invoke-Audit `
    -ScenarioName "local-connector" `
    -ConnectorsJson $connectorPayload `
    -RequireConnectorInstanceCount 5

$null = Invoke-Audit `
    -ScenarioName "manifest-connector" `
    -ManifestUrl "$misubPublicUrl/api/manifest/$misubConnectorProfileId" `
    -ManifestToken $manifestToken `
    -RequireManifestHealthy `
    -RequireConnectorInstanceCount 5

$summaryPath = Join-Path $artifactDir "summary.json"
$summary | ConvertTo-Json -Depth 100 | Set-Content -Path $summaryPath -Encoding UTF8

Write-Host "[runtime] success"
Write-Host "[runtime] summary: $summaryPath"
Write-Host "[runtime] artifacts retained at $artifactDir"
