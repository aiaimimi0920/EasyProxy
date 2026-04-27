param(
    [string]$SmokeId = ("smoke-" + (Get-Date -Format "yyyyMMdd-HHmmss")),
    [string]$Image = "",
    [switch]$KeepArtifacts,
    [switch]$SkipCleanup
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

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

function Invoke-JsonApi {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Uri,
        [hashtable]$Headers,
        [object]$Body
    )

    $invokeParams = @{
        Method      = $Method
        Uri         = $Uri
        Headers     = $Headers
        ContentType = "application/json"
    }
    if ($null -ne $Body) {
        $invokeParams.Body = ($Body | ConvertTo-Json -Depth 100)
    }

    return Invoke-RestMethod @invokeParams
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$deployDir = Split-Path -Parent $scriptDir
$repoRoot = (Resolve-Path (Join-Path $scriptDir "..\..\..\..")).Path
$repoRootDocker = $repoRoot -replace "\\", "/"
$managementPort = Get-FreeTcpPort
$proxyPort = Get-FreeTcpPort
$projectName = ("easyproxysmoke" + ($SmokeId -replace "[^a-zA-Z0-9]", "")).ToLowerInvariant()
$artifactDir = Join-Path $repoRoot ("tmp\\easy-proxy-docker-api-smoke\\" + $SmokeId)
$dataDir = Join-Path $artifactDir "data"
$configPath = Join-Path $artifactDir "config.yaml"
$composePath = Join-Path $artifactDir "docker-compose.yaml"
$evidencePath = Join-Path $artifactDir "evidence.json"
$authHeaderValue = "smoke-secret"

New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

$configYaml = @"
mode: hybrid
log_level: info
skip_cert_verify: true
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
  base_port: 25000
  protocol: http
  username: ""
  password: ""

management:
  enabled: true
  listen: 0.0.0.0:29888
  probe_target: "https://www.google.com/generate_204"
  password: "smoke-secret"

subscription_refresh:
  enabled: false
  interval: 2h0m0s
  timeout: 30s
  health_check_timeout: 1m0s
  drain_timeout: 30s
  min_available_nodes: 0

source_sync:
  enabled: false
  manifest_url: ""
  manifest_token: ""
  refresh_interval: 5m0s
  request_timeout: 15s
  default_direct_proxy_scheme: http
  fallback_subscriptions: []
  connector_runtime:
    enabled: true
    binary_path: "/usr/local/bin/ech-workers"
    working_directory: "/var/lib/easy-proxy/connectors"
    listen_host: "127.0.0.1"
    listen_start_port: 30000
    startup_timeout: 10s
    preferred_ip:
      binary_path: "/usr/local/bin/cfst"
      ip_file_path: "/usr/local/share/cfst/ip.txt"
      working_directory: "/var/lib/easy-proxy/connectors/preferred-ip"
      timeout: 5m0s

connectors: []
subscriptions: []
nodes:
  - name: "dummy-local-http"
    uri: "http://127.0.0.1:9"
"@

$serviceImage = if ([string]::IsNullOrWhiteSpace($Image)) {
    "easyproxy/easy-proxy-monorepo-service:${SmokeId}"
} else {
    $Image
}

$composeBuildBlock = if ([string]::IsNullOrWhiteSpace($Image)) {
@"
    build:
      context: ${repoRootDocker}
      dockerfile: deploy/service/base/Dockerfile
"@
} else {
    ""
}

$composeYaml = @"
services:
  easy-proxy-monorepo-service:
${composeBuildBlock}
    image: ${serviceImage}
    container_name: ${projectName}
    restart: "no"
    ports:
      - "${managementPort}:29888"
      - "${proxyPort}:22323"
    volumes:
      - ./config.yaml:/etc/easy-proxy/config.yaml
      - ./data:/var/lib/easy-proxy
"@

Set-Content -Path $configPath -Value $configYaml -Encoding UTF8
Set-Content -Path $composePath -Value $composeYaml -Encoding UTF8

$headers = @{
    Authorization = $authHeaderValue
}
$baseUrl = "http://127.0.0.1:$managementPort"
$composeArgs = @("-f", $composePath, "-p", $projectName)
$settings = $null
$nodesConfig = $null
$nodesConfigAfterCreate = $null
$connectorsConfig = $null
$connectorsConfigStatus = "not_checked"
$exportText = $null

try {
    Write-Host "[easy-proxy-smoke] artifact dir: $artifactDir"
    Write-Host "[easy-proxy-smoke] management api: $baseUrl"
    $upArgs = @($composeArgs + @("up", "-d"))
    if ([string]::IsNullOrWhiteSpace($Image)) {
        $upArgs += "--build"
    }
    & docker compose @upArgs
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose up failed"
    }

    $deadline = (Get-Date).AddMinutes(8)
    $healthy = $false
    while ((Get-Date) -lt $deadline) {
        try {
            $settings = Invoke-JsonApi -Method GET -Uri "$baseUrl/api/settings" -Headers $headers
            $healthy = $true
            break
        }
        catch {
            Start-Sleep -Seconds 3
        }
    }

    if (-not $healthy) {
        throw "easy-proxy-monorepo-service management API did not become ready before timeout"
    }

    $nodesConfig = Invoke-JsonApi -Method GET -Uri "$baseUrl/api/nodes/config" -Headers $headers
    try {
        $connectorsConfig = Invoke-JsonApi -Method GET -Uri "$baseUrl/api/connectors/config" -Headers $headers
        $connectorsConfigStatus = "ok"
    }
    catch {
        $connectorsConfigStatus = "unavailable"
        $connectorsConfig = [ordered]@{
            error = $_.Exception.Message
        }
    }
    $exportText = Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri "$baseUrl/api/export"
    $createdNode = Invoke-JsonApi -Method POST -Uri "$baseUrl/api/nodes/config" -Headers $headers -Body @{
        name = "dummy-second-http"
        uri  = "http://127.0.0.1:10"
    }
    $nodesConfigAfterCreate = Invoke-JsonApi -Method GET -Uri "$baseUrl/api/nodes/config" -Headers $headers

    & docker compose @composeArgs exec -T easy-proxy-monorepo-service sh -lc "test -f /etc/easy-proxy/config.yaml && test -d /var/lib/easy-proxy && test -x /usr/local/bin/easy-proxy && test -f /var/lib/easy-proxy/data/data.db"
    if ($LASTEXITCODE -ne 0) {
        throw "container contract verification failed"
    }

    $connectorCount = 0
    if ($connectorsConfig -and ($connectorsConfig.PSObject.Properties.Name -contains "connectors")) {
        $connectorCount = @($connectorsConfig.connectors).Count
    }

    $evidence = [ordered]@{
        smokeId = $SmokeId
        verifiedAt = (Get-Date).ToString("o")
        managementBaseUrl = $baseUrl
        proxyPort = $proxyPort
        composePath = $composePath
        configPath = $configPath
        checks = [ordered]@{
            settingsGet = [ordered]@{
                mode = $settings.mode
                listenerPort = $settings.listener_port
                managementListen = $settings.management_listen
                sourceSyncEnabled = $settings.source_sync_enabled
            }
            nodesConfigCount = @($nodesConfig.nodes).Count
            nodesConfigAfterCreateCount = @($nodesConfigAfterCreate.nodes).Count
            createdNodeName = $createdNode.node.name
            connectorsConfigStatus = $connectorsConfigStatus
            connectorsConfigCount = $connectorCount
            exportLength = $exportText.Content.Length
            configMounted = $true
            stateDirMounted = $true
            sqliteCreated = $true
        }
    }

    $evidence | ConvertTo-Json -Depth 100 | Set-Content -Path $evidencePath -Encoding UTF8
    Write-Host "[easy-proxy-smoke] success"
    Write-Host "[easy-proxy-smoke] evidence: $evidencePath"
}
catch {
    Write-Host "[easy-proxy-smoke] failure: $($_.Exception.Message)" -ForegroundColor Red
    & docker compose @composeArgs logs --no-color
    throw
}
finally {
    if (-not $SkipCleanup) {
        & docker compose @composeArgs down -v --remove-orphans
    }

    if ((-not $KeepArtifacts) -and (Test-Path -LiteralPath $artifactDir)) {
        try {
            Remove-Item -LiteralPath $artifactDir -Recurse -Force -ErrorAction Stop
        }
        catch {
            Write-Warning "[easy-proxy-smoke] artifact cleanup failed: $($_.Exception.Message)"
        }
    }
}
