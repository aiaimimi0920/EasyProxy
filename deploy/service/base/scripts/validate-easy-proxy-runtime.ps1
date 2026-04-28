[CmdletBinding()]
param(
    [string]$ValidationId = ("runtime-" + (Get-Date -Format "yyyyMMdd-HHmmss")),
    [string]$Image = "",
    [string]$ConfigPath = "",
    [int]$ScenarioTimeoutSeconds = 720,
    [switch]$KeepArtifacts,
    [switch]$SkipCleanup
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RepoRoot {
    return (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path
}

function Test-DockerImageExists {
    param([Parameter(Mandatory = $true)][string]$ImageName)

    $images = & docker image ls --format "{{.Repository}}:{{.Tag}}" 2>$null
    if ($LASTEXITCODE -ne 0) {
        return $false
    }
    return @($images) -contains $ImageName
}

. (Join-Path (Get-RepoRoot) "scripts\lib\easyproxy-common.ps1")
. (Join-Path (Get-RepoRoot) "scripts\lib\easyproxy-config.ps1")

function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Any, 0)
    $listener.Start()
    try {
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}

function Test-TcpPortRangeAvailable {
    param(
        [int]$Start,
        [int]$RangeSize
    )

    $listeners = New-Object System.Collections.Generic.List[System.Net.Sockets.TcpListener]
    try {
        for ($port = $Start; $port -lt ($Start + $RangeSize); $port++) {
            $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Any, $port)
            try {
                $listener.Start()
                $listeners.Add($listener)
            }
            catch {
                return $false
            }
        }

        return $true
    }
    finally {
        foreach ($listener in $listeners) {
            try {
                $listener.Stop()
            }
            catch {
            }
        }
    }
}

function Get-FreeTcpPortRangeStart {
    param(
        [int]$PreferredStart,
        [int]$RangeSize,
        [int]$Step = 100,
        [int]$MaxAttempts = 200
    )

    if ($RangeSize -lt 1) {
        throw "RangeSize must be >= 1"
    }

    $candidate = $PreferredStart
    for ($attempt = 0; $attempt -lt $MaxAttempts; $attempt++) {
        if (($candidate + $RangeSize - 1) -gt 65535) {
            break
        }
        if (Test-TcpPortRangeAvailable -Start $candidate -RangeSize $RangeSize) {
            return $candidate
        }
        $candidate += $Step
    }

    throw "Unable to find a free TCP port range starting near $PreferredStart (size=$RangeSize)"
}

function Invoke-OptionalRestJson {
    param([string]$Uri)

    try {
        return Invoke-RestMethod -Uri $Uri -Method GET
    }
    catch {
        return $null
    }
}

function Get-NullDevicePath {
    if ($env:OS -eq 'Windows_NT' -or $PSVersionTable.PSEdition -eq 'Desktop') {
        return "NUL"
    }
    return "/dev/null"
}

function Get-CurlCommandName {
    foreach ($name in @("curl.exe", "curl")) {
        $command = Get-Command -Name $name -ErrorAction SilentlyContinue
        if ($null -ne $command) {
            return $command.Source
        }
    }
    throw "curl is required for runtime proxy validation"
}

function Remove-DockerContainerSilently {
    param([string]$ContainerName)

    try {
        & docker rm -f $ContainerName *> $null
    }
    catch {
    }
}

function Wait-ManagementReady {
    param(
        [string]$BaseUrl,
        [int]$TimeoutSeconds
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $settings = Invoke-OptionalRestJson -Uri "$BaseUrl/api/settings"
        if ($null -ne $settings) {
            return $settings
        }
        Start-Sleep -Seconds 3
    }

    throw "Timed out waiting for management API at $BaseUrl"
}

function Wait-ScenarioState {
    param(
        [string]$BaseUrl,
        [int]$TimeoutSeconds,
        [scriptblock]$Ready
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $nodes = Invoke-OptionalRestJson -Uri "$BaseUrl/api/nodes?prefer_available=1"
        $sourceSync = Invoke-OptionalRestJson -Uri "$BaseUrl/api/source-sync/status"
        if ($null -ne $nodes) {
            $state = [pscustomobject]@{
                Nodes      = $nodes
                SourceSync = $sourceSync
            }
            if (& $Ready $state) {
                return $state
            }
        }
        Start-Sleep -Seconds 5
    }

    throw "Timed out waiting for scenario readiness at $BaseUrl"
}

function Test-Proxy204 {
    param([string]$ProxyUrl)

    $probeTargets = @(
        "https://www.google.com/generate_204",
        "https://connectivitycheck.gstatic.com/generate_204",
        "https://cp.cloudflare.com/generate_204",
        "https://www.msftconnecttest.com/connecttest.txt"
    )
    $curlCommand = Get-CurlCommandName
    $nullDevice = Get-NullDevicePath

    $attemptErrors = @()
    foreach ($target in $probeTargets) {
        $code = & $curlCommand -s -k -o $nullDevice -w "%{http_code}" --max-time 25 -x $ProxyUrl $target
        if ($LASTEXITCODE -eq 0 -and ($code -eq "204" -or $code -eq "200")) {
            return [int]$code
        }
        $attemptErrors += "$target(code=$code exit=$LASTEXITCODE)"
    }

    throw "Proxy check failed for $ProxyUrl after probing all targets: $($attemptErrors -join '; ')"
}

function New-ScenarioConfig {
    param(
        [int]$MultiPortBase,
        [string]$Body
    )

    return @"
mode: hybrid
log_level: info
skip_cert_verify: false
database_path: /var/lib/easy-proxy/data/data.db

listener:
  address: 0.0.0.0
  port: 22323
  protocol: http
  username: ""
  password: ""

pool:
  mode: auto
  failure_threshold: 3
  blacklist_duration: 24h

multi_port:
  address: 0.0.0.0
  base_port: ${MultiPortBase}
  protocol: http
  username: ""
  password: ""

management:
  enabled: true
  listen: 0.0.0.0:29888
  probe_target: "https://www.google.com/generate_204"
  password: ""

subscription_refresh:
  enabled: false
  interval: 24h0m0s
  timeout: 30s
  health_check_timeout: 1m0s
  drain_timeout: 30s
  min_available_nodes: 0

$Body
"@
}

function Convert-StringListToYamlSequence {
    param([string[]]$Items)

    $normalized = @($Items | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | ForEach-Object { $_.Trim() })
    if ($normalized.Count -eq 0) {
        return "[]"
    }

    return (($normalized | ForEach-Object { '  - "{0}"' -f ($_ -replace '"', '\"') }) -join [Environment]::NewLine)
}

function Convert-ScenarioEvidence {
    param(
        [string]$Name,
        [object]$State,
        [int]$PoolPort,
        [int[]]$ValidatedPorts
    )

    $availableNodes = @($State.Nodes.nodes | Where-Object { $_.available -eq $true })
    $nodeChecks = @()
    $successfulNodeChecks = 0
    $stableAvailableNode = $null
    $stableAvailableURIs = New-Object System.Collections.Generic.List[string]
    foreach ($port in $ValidatedPorts) {
        $checkResult = $null
        $checkError = ""
        try {
            $checkResult = (Test-Proxy204 ("http://127.0.0.1:{0}" -f $port))
            $successfulNodeChecks++
            $matchingNode = @($availableNodes | Where-Object { [int]$_.port -eq $port } | Select-Object -First 1)
            if ($matchingNode -and -not [string]::IsNullOrWhiteSpace([string]$matchingNode.uri)) {
                $stableAvailableURIs.Add([string]$matchingNode.uri)
            }
            if ($null -eq $stableAvailableNode) {
                $stableAvailableNode = $matchingNode
            }
        }
        catch {
            $checkError = $_.Exception.Message
        }

        $nodeChecks += [pscustomobject]@{
            port = $port
            google_generate_204 = $checkResult
            error = $checkError
        }
    }

    return [ordered]@{
        scenario = $Name
        verified_at = (Get-Date).ToString("o")
        total_nodes = [int]$State.Nodes.total_nodes
        available_nodes = [int]$State.Nodes.available_nodes
        available_node_names = @($availableNodes | Select-Object -First 5 | ForEach-Object { [string]$_.name })
        first_available_uri = if ($availableNodes.Count -gt 0) { [string]$availableNodes[0].uri } else { "" }
        first_stable_available_uri = if ($null -ne $stableAvailableNode) { [string]$stableAvailableNode.uri } else { "" }
        stable_available_uris = @($stableAvailableURIs | Select-Object -Unique)
        source_sync = $State.SourceSync
        pool_google_generate_204 = (Test-Proxy204 ("http://127.0.0.1:{0}" -f $PoolPort))
        stable_node_proxy_count = $successfulNodeChecks
        node_google_generate_204 = $nodeChecks
    }
}

function Run-Scenario {
    param(
        [string]$Name,
        [int]$ScenarioIndex,
        [string]$ConfigYaml,
        [scriptblock]$Ready,
        [scriptblock]$Assert
    )

    $scenarioDir = Join-Path $artifactDir $Name
    $dataDir = Join-Path $scenarioDir "data"
    $configPath = Join-Path $scenarioDir "config.yaml"
    $evidencePath = Join-Path $scenarioDir "evidence.json"
    $multiRangeSize = 81
    $preferredMultiBase = 34000 + ($ScenarioIndex * 100)
    $multiBase = Get-FreeTcpPortRangeStart -PreferredStart $preferredMultiBase -RangeSize $multiRangeSize
    $multiRangeEnd = $multiBase + $multiRangeSize - 1
    $ConfigYaml = $ConfigYaml -replace '(?m)^  base_port: \d+$', "  base_port: $multiBase"
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    Set-Content -Path $configPath -Value $ConfigYaml -Encoding UTF8

    $managementPort = Get-FreeTcpPort
    $poolPort = Get-FreeTcpPort
    $containerName = ("easyproxy-monorepo-runtime-" + $ValidationId + "-" + $Name).ToLowerInvariant().Replace("_", "-")

    Remove-DockerContainerSilently -ContainerName $containerName

    $dockerArgs = @(
        "run", "-d",
        "--name", $containerName,
        "-p", "${managementPort}:29888",
        "-p", "${poolPort}:22323",
        "-p", "${multiBase}-${multiRangeEnd}:${multiBase}-${multiRangeEnd}",
        "-v", ("{0}:/etc/easy-proxy/config.yaml" -f (Resolve-Path $configPath).Path),
        "-v", ("{0}:/var/lib/easy-proxy" -f (Resolve-Path $dataDir).Path),
        $effectiveImage
    )

    Write-Host "[runtime:$Name] starting container $containerName"
    & docker @dockerArgs | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "docker run failed for $Name"
    }

    try {
        $baseUrl = "http://127.0.0.1:$managementPort"
        $null = Wait-ManagementReady -BaseUrl $baseUrl -TimeoutSeconds 180
        $state = Wait-ScenarioState -BaseUrl $baseUrl -TimeoutSeconds $ScenarioTimeoutSeconds -Ready $Ready
        $availablePorts = @($state.Nodes.nodes | Where-Object { $_.available -eq $true } | Select-Object -First 20 | ForEach-Object { [int]$_.port })
        if ($availablePorts.Count -lt 1) {
            throw "Scenario $Name reported no available node ports"
        }

        $evidence = Convert-ScenarioEvidence -Name $Name -State $state -PoolPort $poolPort -ValidatedPorts $availablePorts
        & $Assert $evidence
        $evidence | ConvertTo-Json -Depth 100 | Set-Content -Path $evidencePath -Encoding UTF8
        $script:summary += $evidence

        if ([string]::IsNullOrWhiteSpace($script:directProxyUri) -and -not [string]::IsNullOrWhiteSpace([string]$evidence.first_stable_available_uri)) {
            $script:directProxyUri = [string]$evidence.first_stable_available_uri
        }
        if ($evidence.stable_available_uris) {
            foreach ($candidateUri in @($evidence.stable_available_uris)) {
                if ([string]::IsNullOrWhiteSpace([string]$candidateUri)) {
                    continue
                }
                if (-not ($script:directProxyCandidates -contains [string]$candidateUri)) {
                    $script:directProxyCandidates += [string]$candidateUri
                }
            }
        }

        Write-Host "[runtime:$Name] passed"
    }
    catch {
        Write-Host "[runtime:$Name] failed: $($_.Exception.Message)" -ForegroundColor Red
        docker logs $containerName | Out-Host
        throw
    }
    finally {
        if (-not $SkipCleanup) {
            Remove-DockerContainerSilently -ContainerName $containerName
        }
    }
}

$repoRoot = Get-RepoRoot
$effectiveConfigPath = $ConfigPath
if ([string]::IsNullOrWhiteSpace($effectiveConfigPath)) {
    $effectiveConfigPath = Join-Path $repoRoot "config.yaml"
}
$effectiveImage = $Image
if ([string]::IsNullOrWhiteSpace($effectiveImage)) {
    $effectiveImage = "easyproxy/easy-proxy-monorepo-service:${ValidationId}"
    if (-not (Test-DockerImageExists -ImageName $effectiveImage)) {
        Write-Host "[runtime] building validation image $effectiveImage"
        & docker build -f (Join-Path $repoRoot "deploy/service/base/Dockerfile") -t $effectiveImage $repoRoot
        if ($LASTEXITCODE -ne 0) {
            throw "docker build failed for runtime validation image $effectiveImage"
        }
    }
}
$artifactDir = Join-Path $repoRoot ("tmp\easy-proxy-runtime-validation\" + $ValidationId)
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null

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
$workerServerIp = ""
$configuredLocalSubscriptions = @(Get-EasyProxyConfigValue -Object $serviceRuntime -Name 'subscriptions' -Default @())
if ($configuredLocalSubscriptions.Count -lt 1) {
    throw "config.yaml does not define any serviceBase.runtime.subscriptions for local subscription validation"
}
$configuredLocalSubscriptionsYaml = Convert-StringListToYamlSequence -Items $configuredLocalSubscriptions

$summary = @()
$directProxyUri = ""
$directProxyCandidates = @()

Run-Scenario -Name "local-subscription" -ScenarioIndex 0 -ConfigYaml (New-ScenarioConfig 25000 @"
source_sync:
  enabled: false
  manifest_url: ""
  manifest_token: ""
  refresh_interval: 24h0m0s
  request_timeout: 15s
  default_direct_proxy_scheme: http
  fallback_subscriptions: []
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 30s
connectors: []
subscriptions:
$configuredLocalSubscriptionsYaml
"@) -Ready {
    param($state)
    return ($state.Nodes.total_nodes -gt 0 -and $state.Nodes.available_nodes -gt 0)
} -Assert {
    param($evidence)
    if ([int]$evidence.available_nodes -lt 1) {
        throw "local-subscription did not produce available nodes"
    }
}

Run-Scenario -Name "manifest-subscription" -ScenarioIndex 1 -ConfigYaml (New-ScenarioConfig 25200 @"
source_sync:
  enabled: true
  manifest_url: "$misubPublicUrl/api/manifest/aggregator-global"
  manifest_token: "$manifestToken"
  refresh_interval: 24h0m0s
  request_timeout: 15s
  default_direct_proxy_scheme: http
  fallback_subscriptions: []
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 30s
connectors: []
subscriptions: []
"@) -Ready {
    param($state)
    return (
        $null -ne $state.SourceSync -and
        $state.SourceSync.manifest_healthy -eq $true -and
        [int]$state.SourceSync.manifest_source_count -gt 0 -and
        $state.Nodes.total_nodes -gt 0 -and
        $state.Nodes.available_nodes -gt 0
    )
} -Assert {
    param($evidence)
    if (-not $evidence.source_sync.manifest_healthy) {
        throw "manifest-subscription manifest was not healthy"
    }
    if ([int]$evidence.source_sync.manifest_source_count -lt 1) {
        throw "manifest-subscription missing manifest sources"
    }
    if ([int]$evidence.stable_node_proxy_count -lt 1) {
        throw "manifest-subscription did not yield a stable direct node proxy"
    }
}

if ([string]::IsNullOrWhiteSpace($directProxyUri) -and $directProxyCandidates.Count -gt 0) {
    $directProxyUri = $directProxyCandidates[0]
}
if ([string]::IsNullOrWhiteSpace($directProxyUri)) {
    throw "No reusable stable direct proxy URI was discovered during subscription validation"
}
if ($directProxyCandidates.Count -eq 0) {
    $directProxyCandidates = @($directProxyUri)
}

$directProxyValidated = $false
$directProxyErrors = @()
foreach ($candidateUri in $directProxyCandidates) {
    try {
        Run-Scenario -Name "local-direct-proxy" -ScenarioIndex 2 -ConfigYaml (New-ScenarioConfig 25100 @"
source_sync:
  enabled: false
  manifest_url: ""
  manifest_token: ""
  refresh_interval: 24h0m0s
  request_timeout: 15s
  default_direct_proxy_scheme: http
  fallback_subscriptions: []
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 30s
connectors: []
subscriptions: []
nodes:
  - name: "seed-direct-proxy"
    uri: "$candidateUri"
"@) -Ready {
            param($state)
            return ($state.Nodes.total_nodes -gt 0 -and $state.Nodes.available_nodes -gt 0)
        } -Assert {
            param($evidence)
            if ([int]$evidence.available_nodes -lt 1) {
                throw "local-direct-proxy did not become available"
            }
            if ([int]$evidence.stable_node_proxy_count -lt 1) {
                throw "local-direct-proxy did not yield a stable direct node proxy"
            }
        }
        $directProxyValidated = $true
        $directProxyUri = $candidateUri
        break
    }
    catch {
        $directProxyErrors += "$candidateUri => $($_.Exception.Message)"
    }
}
if (-not $directProxyValidated) {
    throw "local-direct-proxy failed for all candidate URIs: $($directProxyErrors -join '; ')"
}

Run-Scenario -Name "fallback-subscription" -ScenarioIndex 3 -ConfigYaml (New-ScenarioConfig 25300 @"
source_sync:
  enabled: true
  manifest_url: "http://127.0.0.1:1/api/manifest/broken"
  manifest_token: "$manifestToken"
  refresh_interval: 24h0m0s
  request_timeout: 5s
  default_direct_proxy_scheme: http
  fallback_subscriptions:
    - "https://sub.aiaimimi.com/subs/clash.yaml"
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 10s
connectors: []
subscriptions: []
"@) -Ready {
    param($state)
    return (
        $null -ne $state.SourceSync -and
        $state.SourceSync.manifest_healthy -eq $false -and
        $state.SourceSync.fallback_active -eq $true -and
        [int]$state.SourceSync.fallback_source_count -gt 0 -and
        $state.Nodes.total_nodes -gt 0 -and
        $state.Nodes.available_nodes -gt 0
    )
} -Assert {
    param($evidence)
    if ($evidence.source_sync.manifest_healthy) {
        throw "fallback-subscription manifest unexpectedly healthy"
    }
    if (-not $evidence.source_sync.fallback_active) {
        throw "fallback-subscription did not activate fallback"
    }
    if ([int]$evidence.stable_node_proxy_count -lt 1) {
        throw "fallback-subscription did not yield a stable direct node proxy"
    }
}

Run-Scenario -Name "local-connector" -ScenarioIndex 4 -ConfigYaml (New-ScenarioConfig 25400 @"
source_sync:
  enabled: false
  manifest_url: ""
  manifest_token: ""
  refresh_interval: 24h0m0s
  request_timeout: 15s
  default_direct_proxy_scheme: http
  fallback_subscriptions: []
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 30s
connectors:
  - name: "ECH Local Preferred"
    input: "$workerUrl"
    enabled: true
    connector_type: "ech_worker"
    connector_config:
      local_protocol: "socks5"
      access_token: "$workerAccessToken"
      server_ip: "$workerServerIp"
subscriptions: []
"@) -Ready {
    param($state)
    return (
        $null -ne $state.SourceSync -and
        [int]$state.SourceSync.connector_source_count -gt 0 -and
        [int]$state.SourceSync.connector_instance_count -gt 0 -and
        $state.Nodes.total_nodes -gt 0 -and
        $state.Nodes.available_nodes -gt 0
    )
} -Assert {
    param($evidence)
    if ([int]$evidence.available_nodes -lt 1) {
        throw "local-connector did not yield an available route"
    }
    if ([int]$evidence.source_sync.connector_instance_count -lt 5) {
        throw "local-connector did not fan out into at least 5 preferred connector instances"
    }
    if ([int]$evidence.stable_node_proxy_count -lt 1) {
        throw "local-connector did not yield a stable direct node proxy"
    }
}

Run-Scenario -Name "manifest-connector" -ScenarioIndex 5 -ConfigYaml (New-ScenarioConfig 25500 @"
source_sync:
  enabled: true
  manifest_url: "$misubPublicUrl/api/manifest/$misubConnectorProfileId"
  manifest_token: "$manifestToken"
  refresh_interval: 24h0m0s
  request_timeout: 15s
  default_direct_proxy_scheme: http
  fallback_subscriptions: []
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 30s
connectors: []
subscriptions: []
"@) -Ready {
    param($state)
    return (
        $null -ne $state.SourceSync -and
        $state.SourceSync.manifest_healthy -eq $true -and
        [int]$state.SourceSync.connector_source_count -gt 0 -and
        [int]$state.SourceSync.connector_instance_count -gt 0 -and
        $state.Nodes.total_nodes -gt 0 -and
        $state.Nodes.available_nodes -gt 0
    )
} -Assert {
    param($evidence)
    if (-not $evidence.source_sync.manifest_healthy) {
        throw "manifest-connector manifest was not healthy"
    }
    if ([int]$evidence.source_sync.connector_source_count -lt 1) {
        throw "manifest-connector missing connector sources"
    }
    if ([int]$evidence.source_sync.connector_instance_count -lt 5) {
        throw "manifest-connector did not fan out into at least 5 preferred connector instances"
    }
    if ([int]$evidence.stable_node_proxy_count -lt 1) {
        throw "manifest-connector did not yield a stable direct node proxy"
    }
}

$summaryPath = Join-Path $artifactDir "summary.json"
$summary | ConvertTo-Json -Depth 100 | Set-Content -Path $summaryPath -Encoding UTF8

Write-Host "[runtime] success"
Write-Host "[runtime] summary: $summaryPath"

if (-not $KeepArtifacts) {
    Write-Host "[runtime] artifacts retained at $artifactDir"
}
