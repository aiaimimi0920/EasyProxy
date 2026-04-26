[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$ProfileId = "easyproxies-ech-test",
    [string]$WorkerUrl = "",
    [string]$CustomDomainUrl = "https://proxyservice-ech-workers.aiaimimi.com:443",
    [string]$AccessToken = "",
    [string]$MiSubBaseUrl = "",
    [string]$AdminPassword = "",
    [string]$ManifestToken = "",
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

function Get-RepoRoot {
    return (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
}

function Read-JsonFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        return $null
    }
    return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
}

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

function Get-ManagedProfileFromArchive {
    param(
        [Parameter(Mandatory = $true)]$Archive,
        [Parameter(Mandatory = $true)][string]$ProfileId
    )

    $profiles = $Archive.connector_registry.managed_test_profiles
    if ($null -eq $profiles) {
        return $null
    }

    foreach ($property in $profiles.psobject.Properties) {
        $candidate = $property.Value
        if ($null -eq $candidate) {
            continue
        }

        $candidateCustomId = [string](Get-ObjectPropertyValue -Object $candidate -Name "custom_id" -Default "")
        $candidateId = [string](Get-ObjectPropertyValue -Object $candidate -Name "id" -Default "")
        if ($candidateCustomId -eq $ProfileId -or $candidateId -eq $ProfileId -or $property.Name -eq $ProfileId -or $property.Name -eq ($ProfileId -replace "-", "_")) {
            return $candidate
        }
    }

    return $null
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
    $Value | ConvertTo-Json -Depth 100 | Set-Content -LiteralPath $Path -Encoding UTF8
}

function Normalize-SelectedRows {
    param(
        [Parameter(Mandatory = $true)]$Rows,
        [Parameter(Mandatory = $true)][int]$TopCount
    )

    $normalized = foreach ($row in $Rows) {
        $ip = [string](Get-ObjectPropertyValue -Object $row -Name "IP 地址" -Default "")
        if ([string]::IsNullOrWhiteSpace($ip)) {
            continue
        }

        $latencyText = [string](Get-ObjectPropertyValue -Object $row -Name "平均延迟" -Default "99999")
        $lossText = [string](Get-ObjectPropertyValue -Object $row -Name "丢包率" -Default "1")
        $speedText = [string](Get-ObjectPropertyValue -Object $row -Name "下载速度(MB/s)" -Default "0")
        $coloText = [string](Get-ObjectPropertyValue -Object $row -Name "地区码" -Default "")

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

$repoRoot = Get-RepoRoot
$archivePath = Join-Path $repoRoot "AIRead\密钥\ProxyService\MiSub密钥.json"
$archive = Read-JsonFile -Path $archivePath
$managedProfile = $null
$workerUrlSource = "explicit"
if ($null -ne $archive) {
    $managedProfile = Get-ManagedProfileFromArchive -Archive $archive -ProfileId $ProfileId
}

if ([string]::IsNullOrWhiteSpace($MiSubBaseUrl) -and $null -ne $archive -and (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object $archive -Name "production_runtime") -Name "base_url")) {
    $MiSubBaseUrl = [string](Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object $archive -Name "production_runtime") -Name "base_url")
}
if ([string]::IsNullOrWhiteSpace($AdminPassword) -and $null -ne $archive -and (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object $archive -Name "runtime_secrets") -Name "required") -Name "ADMIN_PASSWORD")) {
    $AdminPassword = [string](Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object $archive -Name "runtime_secrets") -Name "required") -Name "ADMIN_PASSWORD")
}
if ([string]::IsNullOrWhiteSpace($ManifestToken) -and $null -ne $archive -and (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object $archive -Name "runtime_secrets") -Name "required") -Name "MANIFEST_TOKEN")) {
    $ManifestToken = [string](Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object (Get-ObjectPropertyValue -Object $archive -Name "runtime_secrets") -Name "required") -Name "MANIFEST_TOKEN")
}
if ([string]::IsNullOrWhiteSpace($WorkerUrl) -and $PreferCustomDomain) {
    $WorkerUrl = $CustomDomainUrl
    $workerUrlSource = "custom_domain_default"
}
if ($null -ne $managedProfile -and $managedProfile.sources -and $managedProfile.sources.Count -gt 0) {
    $defaultSource = $managedProfile.sources[0]
    if ([string]::IsNullOrWhiteSpace($WorkerUrl) -and $defaultSource.input) {
        $WorkerUrl = [string]$defaultSource.input
        $workerUrlSource = "archive_managed_source"
    }
    if ([string]::IsNullOrWhiteSpace($AccessToken) -and $defaultSource.access_token) {
        $AccessToken = [string]$defaultSource.access_token
    }
}
if (-not [string]::IsNullOrWhiteSpace($WorkerUrl) -and $workerUrlSource -eq "explicit") {
    $workerUrlSource = "explicit"
}

if ([string]::IsNullOrWhiteSpace($WorkerUrl)) {
    throw "WorkerUrl is required. Pass -WorkerUrl or archive defaults must exist in $archivePath"
}
if ([string]::IsNullOrWhiteSpace($AccessToken)) {
    throw "AccessToken is required. Pass -AccessToken or archive defaults must exist in $archivePath"
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

$runId = Get-Date -Format "yyyyMMdd-HHmmss"
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
$selectedRows = Normalize-SelectedRows -Rows $rows -TopCount $TopCount
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
Write-JsonFile -Path (Join-Path $artifactDir "selected-sources.json") -Value $selectedSources

if ($ApplyToMiSub) {
    if ([string]::IsNullOrWhiteSpace($MiSubBaseUrl)) {
        throw "MiSubBaseUrl is required when -ApplyToMiSub is used"
    }
    if ([string]::IsNullOrWhiteSpace($AdminPassword)) {
        throw "AdminPassword is required when -ApplyToMiSub is used"
    }

    $clientState = New-JsonHttpClient
    try {
        $loginResponse = Invoke-JsonRequest -ClientState $clientState -Method POST -Url "$MiSubBaseUrl/api/login" -Body @{ password = $AdminPassword }
        Write-JsonFile -Path (Join-Path $artifactDir "login-response.json") -Value $loginResponse

        $dataResponse = Invoke-JsonRequest -ClientState $clientState -Method GET -Url "$MiSubBaseUrl/api/data"
        Write-JsonFile -Path (Join-Path $artifactDir "data-before-update.json") -Value $dataResponse

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
        Write-JsonFile -Path (Join-Path $artifactDir "misub-update-payload.json") -Value $updatePayload

        if ($PSCmdlet.ShouldProcess("$MiSubBaseUrl/api/misubs", "Update ECH preferred sources for $ProfileId")) {
            $updateResponse = Invoke-JsonRequest -ClientState $clientState -Method POST -Url "$MiSubBaseUrl/api/misubs" -Body $updatePayload
            Write-JsonFile -Path (Join-Path $artifactDir "misub-update-response.json") -Value $updateResponse

            $summary.applied_to_misub = $true
            $summary.replaced_source_ids = $existingSourceIds
            $summary.updated_profile_manual_nodes = $updatedManualNodes

            $afterDataResponse = Invoke-JsonRequest -ClientState $clientState -Method GET -Url "$MiSubBaseUrl/api/data"
            Write-JsonFile -Path (Join-Path $artifactDir "data-after-update.json") -Value $afterDataResponse
        }
    } finally {
        if ($null -ne $clientState -and $null -ne $clientState.Client) {
            $clientState.Client.Dispose()
        }
    }

    if (-not [string]::IsNullOrWhiteSpace($ManifestToken)) {
        $manifestClient = New-JsonHttpClient
        try {
            $manifestResponse = Invoke-JsonRequest `
                -ClientState $manifestClient `
                -Method GET `
                -Url "$MiSubBaseUrl/api/manifest/$ProfileId" `
                -Headers @{ Authorization = "Bearer $ManifestToken" }
            Write-JsonFile -Path (Join-Path $artifactDir "manifest-after-update.json") -Value $manifestResponse
        } finally {
            $manifestClient.Client.Dispose()
        }
    }

    Write-JsonFile -Path (Join-Path $artifactDir "summary.json") -Value $summary
}

$summary | ConvertTo-Json -Depth 20
