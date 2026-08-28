[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$TopologyPath = "",
    [string]$ProfileId = "",
    [string]$WorkerUrl = "",
    [string]$CustomDomainUrl = "",
    [string]$MiSubBaseUrl = "",
    [int]$TopCount = 5,
    [string]$LocalProtocol = "socks5",
    [string]$SourceIdPrefix = "conn_ech_workers_pref",
    [string]$SourceNamePrefix = "ECH Worker Preferred",
    [string]$SourceGroup = "ECH Connectors",
    [string]$NotesPrefix = "Preferred Cloudflare entry IP",
    [string]$CfstPath = "",
    [string]$IPFilePath = "",
    [string]$ArtifactRoot = "",
    [string]$ReuseResultCsvPath = "",
    [int]$LatencyThreads = 200,
    [int]$LatencySamples = 4,
    [double]$MaxLoss = 0.0,
    [switch]$PreferCustomDomain,
    [switch]$AllIP,
    [switch]$ApplyToMiSub
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Net.Http

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path
. (Join-Path $repoRoot "scripts\lib\easyproxy-common.ps1")
. (Join-Path $repoRoot "scripts\lib\easyproxy-topology.ps1")

function Resolve-OptionalPath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$RepoRoot
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        return ""
    }
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return (Resolve-Path -LiteralPath $Path).Path
    }
    return (Resolve-Path -LiteralPath (Join-Path $RepoRoot $Path)).Path
}

function Get-ObjectPropertyValue {
    param(
        [Parameter(Mandatory = $true)]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        $Default = $null
    )

    if ($null -eq $Object) {
        return $Default
    }

    $property = $Object.psobject.Properties[$Name]
    if ($null -eq $property) {
        return $Default
    }

    return $property.Value
}

function New-JsonHttpClient {
    $cookieJar = New-Object System.Net.CookieContainer
    $handler = New-Object System.Net.Http.HttpClientHandler
    $handler.CookieContainer = $cookieJar
    $handler.UseCookies = $true
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(90)
    return @{
        Client  = $client
        Cookies = $cookieJar
    }
}

function Invoke-JsonRequest {
    param(
        [Parameter(Mandatory = $true)]$ClientState,
        [Parameter(Mandatory = $true)][ValidateSet("GET", "POST")] [string]$Method,
        [Parameter(Mandatory = $true)][string]$Url,
        [object]$Body,
        [hashtable]$Headers
    )

    $client = $ClientState.Client
    $request = [System.Net.Http.HttpRequestMessage]::new(([System.Net.Http.HttpMethod]::new($Method)), $Url)
    if ($Headers) {
        foreach ($headerKey in $Headers.Keys) {
            [void]$request.Headers.TryAddWithoutValidation($headerKey, [string]$Headers[$headerKey])
        }
    }

    if ($null -ne $Body) {
        $jsonBody = $Body | ConvertTo-Json -Depth 100 -Compress
        $request.Content = [System.Net.Http.StringContent]::new($jsonBody, [System.Text.Encoding]::UTF8, "application/json")
    }

    $response = $client.SendAsync($request).GetAwaiter().GetResult()
    $responseText = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    if (-not $response.IsSuccessStatusCode) {
        throw "HTTP $([int]$response.StatusCode) calling $Url`n$responseText"
    }

    if ([string]::IsNullOrWhiteSpace($responseText)) {
        return $null
    }
    return $responseText | ConvertFrom-Json
}

function Write-JsonFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value
    )
    ConvertTo-Json -InputObject $Value -Depth 100 | Set-Content -LiteralPath $Path -Encoding UTF8
}

function Normalize-SelectedRows {
    param(
        [Parameter(Mandatory = $true)]$Rows,
        [Parameter(Mandatory = $true)][int]$TopCount
    )

    $normalized = foreach ($row in $Rows) {
        $properties = @($row.psobject.Properties)
        if ($properties.Count -lt 4) {
            continue
        }

        $ip = [string]$properties[0].Value
        if ([string]::IsNullOrWhiteSpace($ip)) {
            continue
        }

        $latencyText = [string]$properties[1].Value
        $lossText = [string]$properties[2].Value
        $speedText = [string]$properties[3].Value
        $coloText = if ($properties.Count -ge 5) { [string]$properties[4].Value } else { "" }

        [pscustomobject]@{
            ip                 = $ip.Trim()
            average_latency_ms = [double]$latencyText
            loss_rate          = [double]$lossText
            speed_mb_s         = [double]$speedText
            colo               = $coloText.Trim()
        }
    }

    return @($normalized | Sort-Object average_latency_ms, loss_rate | Select-Object -First $TopCount)
}

function New-EchPreferredSource {
    param(
        [Parameter(Mandatory = $true)][int]$Index,
        [Parameter(Mandatory = $true)][string]$WorkerUrl,
        [Parameter(Mandatory = $true)][string]$AccessToken,
        [Parameter(Mandatory = $true)][string]$ServerIP,
        [Parameter(Mandatory = $true)][string]$LocalProtocol,
        [Parameter(Mandatory = $true)][string]$SourceIdPrefix,
        [Parameter(Mandatory = $true)][string]$SourceNamePrefix,
        [Parameter(Mandatory = $true)][string]$SourceGroup,
        [Parameter(Mandatory = $true)][string]$NotesPrefix
    )

    $connectorConfig = [ordered]@{
        local_protocol = $LocalProtocol
        access_token   = $AccessToken
        server_ip      = $ServerIP
    }

    return [ordered]@{
        id               = "${SourceIdPrefix}_${Index}"
        kind             = "connector"
        name             = "$SourceNamePrefix $Index"
        enabled          = $true
        group            = $SourceGroup
        notes            = "$NotesPrefix #$Index"
        input            = $WorkerUrl
        url              = $WorkerUrl
        connector_type   = "ech_worker"
        connector_config = $connectorConfig
        options          = [ordered]@{
            connector_type   = "ech_worker"
            connector_config = $connectorConfig
        }
    }
}

function ConvertTo-EchWorkerUrl {
    param([Parameter(Mandatory = $true)][string]$Url)

    $uri = [System.Uri]$Url.Trim()
    if (-not $uri.IsDefaultPort) {
        return $uri.AbsoluteUri.TrimEnd("/")
    }

    $port = if ($uri.Scheme -eq "https") {
        443
    } elseif ($uri.Scheme -eq "http") {
        80
    } else {
        return $uri.AbsoluteUri.TrimEnd("/")
    }
    $hostName = if ($uri.HostNameType -eq [System.UriHostNameType]::IPv6) {
        "[$($uri.Host)]"
    } else {
        $uri.Host
    }
    $userInfo = if ([string]::IsNullOrWhiteSpace($uri.UserInfo)) { "" } else { "$($uri.UserInfo)@" }
    $pathAndQuery = if ($uri.PathAndQuery -eq "/") { "" } else { $uri.PathAndQuery }
    return "$($uri.Scheme)://${userInfo}${hostName}:${port}${pathAndQuery}$($uri.Fragment)"
}

if ([string]::IsNullOrWhiteSpace($TopologyPath)) {
    $defaultTopologyPath = Join-Path $repoRoot 'topology.yaml'
    if (Test-Path -LiteralPath $defaultTopologyPath) {
        $TopologyPath = $defaultTopologyPath
    }
}
if ([string]::IsNullOrWhiteSpace($TopologyPath)) {
    throw "TopologyPath is required. Initialize topology.yaml or pass its path explicitly."
}
$topology = Read-EasyProxyTopology -TopologyPath $TopologyPath

$workerUrlSource = "explicit"
if ([string]::IsNullOrWhiteSpace($ProfileId)) {
    $ProfileId = [string]$topology.misub.default_profile
}
if ([string]::IsNullOrWhiteSpace($MiSubBaseUrl)) {
    $names = Get-EasyProxyResourceNames -TopologyPath $TopologyPath
    if ([bool]$topology.cloudflare.use_pages_dev) {
        $MiSubBaseUrl = "https://$($names.pages_project).pages.dev"
    }
}
$AccessToken = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.ech_token) -Purpose 'ECH Worker authentication'
$AdminPassword = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.misub_admin_password) -Purpose 'MiSub administration' -Optional
$ManifestToken = Get-EasyProxyEnvironmentValue -Reference ([string]$topology.secrets.misub_manifest_token) -Purpose 'MiSub manifest' -Optional
if ([string]::IsNullOrWhiteSpace($WorkerUrl) -and $PreferCustomDomain -and -not [string]::IsNullOrWhiteSpace($CustomDomainUrl)) {
    $WorkerUrl = ConvertTo-EchWorkerUrl -Url $CustomDomainUrl
    $workerUrlSource = "custom_domain_override"
}
if ([string]::IsNullOrWhiteSpace($ProfileId)) {
    $ProfileId = "easyproxies-ech-runtime"
}
if (-not [string]::IsNullOrWhiteSpace($WorkerUrl) -and $workerUrlSource -eq "explicit") {
    $workerUrlSource = "explicit"
}

if ([string]::IsNullOrWhiteSpace($WorkerUrl)) {
    throw "WorkerUrl is required. Pass -WorkerUrl or use -PreferCustomDomain."
}
if ($TopCount -lt 1) {
    throw "TopCount must be >= 1"
}

if ([string]::IsNullOrWhiteSpace($ArtifactRoot)) {
    $ArtifactRoot = Join-Path $repoRoot "tmp\ech-workers-cloudflare\preferred-ip"
}
if (-not [System.IO.Path]::IsPathRooted($ArtifactRoot)) {
    $ArtifactRoot = Join-Path $repoRoot $ArtifactRoot
}

$runId = "{0}-{1}" -f (Get-Date -Format "yyyyMMdd-HHmmss-fff"), ([Guid]::NewGuid().ToString('N').Substring(0, 8))
$artifactDir = Join-Path $ArtifactRoot $runId
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null

$workerUri = [System.Uri]$WorkerUrl
$workerPort = if ($workerUri.IsDefaultPort) {
    if ($workerUri.Scheme -eq "https") { 443 } else { 80 }
} else {
    $workerUri.Port
}

$resultCsvPath = ""
$speedTestMode = "reused"

if (-not [string]::IsNullOrWhiteSpace($ReuseResultCsvPath)) {
    $resultCsvPath = Resolve-OptionalPath -Path $ReuseResultCsvPath -RepoRoot $repoRoot
    Copy-Item -LiteralPath $resultCsvPath -Destination (Join-Path $artifactDir "result.csv") -Force
    $resultCsvPath = Join-Path $artifactDir "result.csv"
} else {
    if ([string]::IsNullOrWhiteSpace($CfstPath)) {
        $CfstPath = Join-Path $repoRoot "tmp\tools\cfst\cfst.exe"
    } elseif (-not [System.IO.Path]::IsPathRooted($CfstPath)) {
        $CfstPath = Join-Path $repoRoot $CfstPath
    }
    if ([string]::IsNullOrWhiteSpace($IPFilePath)) {
        $IPFilePath = Join-Path (Split-Path -Parent $CfstPath) "ip.txt"
    } elseif (-not [System.IO.Path]::IsPathRooted($IPFilePath)) {
        $IPFilePath = Join-Path $repoRoot $IPFilePath
    }

    if (-not (Test-Path -LiteralPath $CfstPath)) {
        throw "CloudflareSpeedTest binary not found: $CfstPath"
    }
    if (-not (Test-Path -LiteralPath $IPFilePath)) {
        throw "CloudflareSpeedTest IP file not found: $IPFilePath"
    }

    $resultCsvPath = Join-Path $artifactDir "result.csv"
    $cfstArgs = @(
        "-tp", "$workerPort",
        "-dd",
        "-f", $IPFilePath,
        "-n", "$LatencyThreads",
        "-t", "$LatencySamples",
        "-tlr", ([string]::Format([System.Globalization.CultureInfo]::InvariantCulture, "{0:0.00}", $MaxLoss)),
        "-p", "$TopCount",
        "-o", $resultCsvPath
    )
    if ($AllIP) {
        $cfstArgs += "-allip"
    }

    [ordered]@{
        binary = $CfstPath
        args   = $cfstArgs
    } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $artifactDir "speedtest-command.json") -Encoding UTF8

    Push-Location (Split-Path -Parent $CfstPath)
    try {
        & $CfstPath @cfstArgs
        if ($LASTEXITCODE -ne 0) {
            throw "CloudflareSpeedTest failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }

    if (-not (Test-Path -LiteralPath $resultCsvPath)) {
        throw "CloudflareSpeedTest did not produce result.csv"
    }
    $speedTestMode = "fresh"
}

$rows = Import-Csv -LiteralPath $resultCsvPath -Encoding UTF8
$selectedRows = @(Normalize-SelectedRows -Rows $rows -TopCount $TopCount)
if ($selectedRows.Count -eq 0) {
    throw "No preferred Cloudflare IPs were parsed from $resultCsvPath"
}

$selectedSources = @()
for ($index = 0; $index -lt $selectedRows.Count; $index++) {
    $selectedSources += New-EchPreferredSource `
        -Index ($index + 1) `
        -WorkerUrl $WorkerUrl `
        -AccessToken $AccessToken `
        -ServerIP $selectedRows[$index].ip `
        -LocalProtocol $LocalProtocol `
        -SourceIdPrefix $SourceIdPrefix `
        -SourceNamePrefix $SourceNamePrefix `
        -SourceGroup $SourceGroup `
        -NotesPrefix $NotesPrefix
}

$summary = [ordered]@{
    profile_id          = $ProfileId
    worker_url          = $WorkerUrl
    worker_url_source   = $workerUrlSource
    custom_domain_url   = $CustomDomainUrl
    prefer_custom_domain = $PreferCustomDomain.IsPresent
    top_count           = $TopCount
    speedtest_mode      = $speedTestMode
    result_csv          = $resultCsvPath
    selected_ips        = $selectedRows
    selected_source_ids = @($selectedSources | ForEach-Object { $_.id })
    artifact_dir        = $artifactDir
    applied_to_misub    = $false
}
Write-JsonFile -Path (Join-Path $artifactDir "summary.json") -Value $summary

if ($ApplyToMiSub) {
    if ([string]::IsNullOrWhiteSpace($MiSubBaseUrl)) {
        throw "MiSubBaseUrl is required when -ApplyToMiSub is used"
    }
    if ([string]::IsNullOrWhiteSpace($AdminPassword)) {
        throw "AdminPassword is required when -ApplyToMiSub is used"
    }

    $clientState = New-JsonHttpClient
    try {
        $null = Invoke-JsonRequest -ClientState $clientState -Method POST -Url "$MiSubBaseUrl/api/login" -Body @{ password = $AdminPassword }

        $dataResponse = Invoke-JsonRequest -ClientState $clientState -Method GET -Url "$MiSubBaseUrl/api/data"

        $profile = @($dataResponse.profiles | Where-Object {
            ([string](Get-ObjectPropertyValue -Object $_ -Name "customId" -Default "")) -eq $ProfileId -or ([string](Get-ObjectPropertyValue -Object $_ -Name "id" -Default "")) -eq $ProfileId
        }) | Select-Object -First 1
        if ($null -eq $profile) {
            throw "MiSub profile not found: $ProfileId"
        }

        $existingSourceIds = @($dataResponse.misubs | Where-Object {
            ([string](Get-ObjectPropertyValue -Object $_ -Name "id" -Default "")) -like "${SourceIdPrefix}_*"
        } | ForEach-Object { $_.id })

        $retainedMisubs = @($dataResponse.misubs | Where-Object {
            ([string](Get-ObjectPropertyValue -Object $_ -Name "id" -Default "")) -notlike "${SourceIdPrefix}_*"
        })

        $updatedManualNodes = @(
            @($profile.manualNodes | Where-Object { [string]$_ -notlike "${SourceIdPrefix}_*" }) +
            @($selectedSources | ForEach-Object { $_.id })
        )

        $updatedProfile = [ordered]@{}
        foreach ($property in $profile.psobject.Properties) {
            $updatedProfile[$property.Name] = $property.Value
        }
        $updatedProfile.manualNodes = $updatedManualNodes

        $updatedProfiles = foreach ($candidateProfile in $dataResponse.profiles) {
            if (([string](Get-ObjectPropertyValue -Object $candidateProfile -Name "customId" -Default "")) -eq $ProfileId -or ([string](Get-ObjectPropertyValue -Object $candidateProfile -Name "id" -Default "")) -eq $ProfileId) {
                [pscustomobject]$updatedProfile
            } else {
                $candidateProfile
            }
        }

        $updatePayload = [ordered]@{
            misubs   = @($retainedMisubs + $selectedSources)
            profiles = @($updatedProfiles)
        }
        if ($PSCmdlet.ShouldProcess("$MiSubBaseUrl/api/misubs", "Update ECH preferred sources for $ProfileId")) {
            $null = Invoke-JsonRequest -ClientState $clientState -Method POST -Url "$MiSubBaseUrl/api/misubs" -Body $updatePayload

            $summary.applied_to_misub = $true
            $summary.replaced_source_ids = $existingSourceIds
            $summary.updated_profile_manual_nodes = $updatedManualNodes

            $null = Invoke-JsonRequest -ClientState $clientState -Method GET -Url "$MiSubBaseUrl/api/data"
        }
    } finally {
        if ($null -ne $clientState -and $null -ne $clientState.Client) {
            $clientState.Client.Dispose()
        }
    }

    if (-not [string]::IsNullOrWhiteSpace($ManifestToken)) {
        $manifestClient = New-JsonHttpClient
        try {
            $null = Invoke-JsonRequest `
                -ClientState $manifestClient `
                -Method GET `
                -Url "$MiSubBaseUrl/api/manifest/$ProfileId" `
                -Headers @{ Authorization = "Bearer $ManifestToken" }
        } finally {
            $manifestClient.Client.Dispose()
        }
    }

    Write-JsonFile -Path (Join-Path $artifactDir "summary.json") -Value $summary
}

$summary | ConvertTo-Json -Depth 20
