param(
    [ValidateSet(
        "easyproxy",
        "misub-pages",
        "misub-docker",
        "aggregator",
        "ech-workers-cloudflare",
        "build-easyproxy-image",
        "build-ech-workers-image",
        "publish-service-base-config",
        "publish-easyproxy-image",
        "publish-ech-workers-image",
        "publish-core-images"
    )]
    [string]$Project,
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [switch]$InitConfig,
    [switch]$NoBuild,
    [switch]$NoInstall,
    [switch]$SkipRender,
    [switch]$DryRun,
    [switch]$SkipSecretSync,
    [switch]$SkipSecretUpdate,
    [switch]$SkipWorkflowTrigger,
    [switch]$NoCache,
    [switch]$Push,
    [string]$ReleaseTag,
    [string]$GhcrOwner,
    [string]$GhcrUsername,
    [string]$GhcrToken,
    [switch]$LoadOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'lib\easyproxy-common.ps1')
. (Join-Path $PSScriptRoot 'lib\easyproxy-config.ps1')

function Resolve-ConfigPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return $Path
    }

    return (Join-Path (Get-EasyProxyRepoRoot) $Path)
}

function Ensure-ConfigReady {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [switch]$InitIfMissing
    )

    if (Test-Path -LiteralPath $Path) {
        return
    }

    if (-not $InitIfMissing) {
        throw "Missing config file: $Path. Run scripts/init-config.ps1 first or pass -InitConfig."
    }

    $initScript = Join-Path $PSScriptRoot 'init-config.ps1'
    Write-Host "config.yaml not found, initializing from template..." -ForegroundColor Yellow
    Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments @(
        "-ExecutionPolicy", "Bypass",
        "-File", $initScript,
        "-OutputPath", $Path
    ) -FailureMessage "Failed to initialize config.yaml from config.example.yaml"
}

function Assert-ProjectConfigReady {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Project,
        [Parameter(Mandatory = $true)]
        [object]$Config
    )

    $errors = @()

    switch ($Project) {
        "easyproxy" {
            $serviceBase = Get-EasyProxyConfigSection -Config $Config -Name 'serviceBase'
            $runtime = Get-EasyProxyConfigSection -Config $serviceBase -Name 'runtime'
            $sourceSync = Get-EasyProxyConfigSection -Config $runtime -Name 'source_sync'
            $manifestUrl = [string](Get-EasyProxyConfigValue -Object $sourceSync -Name 'manifest_url' -Default '')
            if ($manifestUrl -match 'example\.com') {
                $errors += "serviceBase.runtime.source_sync.manifest_url still uses an example domain."
            }
        }
        "misub-pages" {
            $misub = Get-EasyProxyConfigSection -Config $Config -Name 'misub'
            $pages = Get-EasyProxyConfigSection -Config $misub -Name 'pages'
            $env = Get-EasyProxyConfigSection -Config $pages -Name 'env'
            $adminPassword = [string](Get-EasyProxyConfigValue -Object $env -Name 'ADMIN_PASSWORD' -Default '')
            $cookieSecret = [string](Get-EasyProxyConfigValue -Object $env -Name 'COOKIE_SECRET' -Default '')
            if ($adminPassword -like 'change_me*' -or [string]::IsNullOrWhiteSpace($adminPassword)) {
                $errors += "misub.pages.env.ADMIN_PASSWORD must be set to a strong value."
            }
            if ($cookieSecret -like 'change_me*' -or [string]::IsNullOrWhiteSpace($cookieSecret)) {
                $errors += "misub.pages.env.COOKIE_SECRET must be set to a stable random value."
            }
        }
        "misub-docker" {
            $misub = Get-EasyProxyConfigSection -Config $Config -Name 'misub'
            $docker = Get-EasyProxyConfigSection -Config $misub -Name 'docker'
            $env = Get-EasyProxyConfigSection -Config $docker -Name 'env'
            $adminPassword = [string](Get-EasyProxyConfigValue -Object $env -Name 'ADMIN_PASSWORD' -Default '')
            $cookieSecret = [string](Get-EasyProxyConfigValue -Object $env -Name 'COOKIE_SECRET' -Default '')
            if ($adminPassword -like 'change_me*' -or [string]::IsNullOrWhiteSpace($adminPassword)) {
                $errors += "misub.docker.env.ADMIN_PASSWORD must be set to a strong value."
            }
            if ($cookieSecret -like 'change_me*' -or [string]::IsNullOrWhiteSpace($cookieSecret)) {
                $errors += "misub.docker.env.COOKIE_SECRET must be set to a stable random value."
            }
        }
        "ech-workers-cloudflare" {
            $worker = Get-EasyProxyConfigSection -Config $Config -Name 'echWorkersCloudflare'
            $secrets = Get-EasyProxyConfigSection -Config $worker -Name 'secrets'
            $echToken = [string](Get-EasyProxyConfigValue -Object $secrets -Name 'ECH_TOKEN' -Default '')
            if ([string]::IsNullOrWhiteSpace($echToken)) {
                $errors += "echWorkersCloudflare.secrets.ECH_TOKEN is empty."
            }
        }
        "publish-service-base-config" {
            $distribution = Get-EasyProxyConfigSection -Config $Config -Name 'distribution'
            $serviceBaseDistribution = Get-EasyProxyConfigSection -Config $distribution -Name 'serviceBase'
            $accountId = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'accountId' -Default '')
            $bucket = [string](Get-EasyProxyConfigValue -Object $serviceBaseDistribution -Name 'bucket' -Default '')
            if ([string]::IsNullOrWhiteSpace($accountId)) {
                $errors += "distribution.serviceBase.accountId must be set."
            }
            if ([string]::IsNullOrWhiteSpace($bucket)) {
                $errors += "distribution.serviceBase.bucket must be set."
            }
        }
    }

    if ($errors.Count -gt 0) {
        $joined = ($errors | ForEach-Object { " - $_" }) -join [Environment]::NewLine
        throw "Config validation failed for ${Project}:`n$joined"
    }
}

if ([string]::IsNullOrWhiteSpace($Project)) {
    throw "Missing -Project. Supported values: easyproxy, misub-pages, misub-docker, aggregator, ech-workers-cloudflare, build-easyproxy-image, build-ech-workers-image, publish-service-base-config, publish-easyproxy-image, publish-ech-workers-image, publish-core-images"
}

$resolvedConfigPath = Resolve-ConfigPath -Path $ConfigPath
$publishProjects = @("publish-easyproxy-image", "publish-ech-workers-image", "publish-core-images")
$config = $null

if ($Project -notin $publishProjects) {
    Ensure-ConfigReady -Path $resolvedConfigPath -InitIfMissing:$InitConfig
    $config = Read-EasyProxyConfig -ConfigPath $resolvedConfigPath
    Assert-ProjectConfigReady -Project $Project -Config $config
}
elseif ((Test-Path -LiteralPath $resolvedConfigPath)) {
    $config = Read-EasyProxyConfig -ConfigPath $resolvedConfigPath
}

switch ($Project) {
    "easyproxy" {
        $scriptPath = Join-Path $PSScriptRoot 'deploy-easyproxy.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath)
        if ($NoBuild) { $args += "-NoBuild" }
        if ($SkipRender) { $args += "-SkipRender" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "easyproxy deploy failed"
        break
    }
    "misub-pages" {
        $scriptPath = Join-Path $PSScriptRoot 'deploy-misub.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath, "-Mode", "pages")
        if ($NoInstall) { $args += "-NoInstall" }
        if ($NoBuild) { $args += "-NoBuild" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "misub pages deploy failed"
        break
    }
    "misub-docker" {
        $scriptPath = Join-Path $PSScriptRoot 'deploy-misub.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath, "-Mode", "docker")
        if ($NoBuild) { $args += "-NoBuild" }
        if ($SkipRender) { $args += "-SkipRender" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "misub docker deploy failed"
        break
    }
    "aggregator" {
        $scriptPath = Join-Path $PSScriptRoot 'deploy-aggregator.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath)
        if ($SkipSecretUpdate) { $args += "-SkipSecretUpdate" }
        if ($SkipWorkflowTrigger) { $args += "-SkipWorkflowTrigger" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "aggregator deploy failed"
        break
    }
    "ech-workers-cloudflare" {
        $scriptPath = Join-Path $PSScriptRoot 'deploy-ech-workers-cloudflare.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath)
        if ($DryRun) { $args += "-DryRun" }
        if ($SkipRender) { $args += "-SkipRender" }
        if ($SkipSecretSync) { $args += "-SkipSecretSync" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "ech-workers-cloudflare deploy failed"
        break
    }
    "build-easyproxy-image" {
        $scriptPath = Join-Path $PSScriptRoot 'build-easyproxy-image.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath)
        if ($NoCache) { $args += "-NoCache" }
        if ($Push) { $args += "-Push" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "build easyproxy image failed"
        break
    }
    "build-ech-workers-image" {
        $scriptPath = Join-Path $PSScriptRoot 'build-ech-workers-image.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath)
        if ($NoCache) { $args += "-NoCache" }
        if ($Push) { $args += "-Push" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "build ech-workers image failed"
        break
    }
    "publish-easyproxy-image" {
        $scriptPath = Join-Path $PSScriptRoot 'publish-ghcr-images.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath, "-Target", "easyproxy")
        if (-not [string]::IsNullOrWhiteSpace($ReleaseTag)) { $args += @("-ReleaseTag", $ReleaseTag) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrOwner)) { $args += @("-GhcrOwner", $GhcrOwner) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrUsername)) { $args += @("-GhcrUsername", $GhcrUsername) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrToken)) { $args += @("-GhcrToken", $GhcrToken) }
        if ($NoCache) { $args += "-NoCache" }
        if ($LoadOnly) { $args += "-LoadOnly" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "publish easyproxy image failed"
        break
    }
    "publish-service-base-config" {
        $scriptPath = Join-Path $PSScriptRoot 'publish-service-base-config.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath)
        if (-not [string]::IsNullOrWhiteSpace($ReleaseTag)) { $args += @("-ReleaseVersion", $ReleaseTag) }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "publish service-base config failed"
        break
    }
    "publish-ech-workers-image" {
        $scriptPath = Join-Path $PSScriptRoot 'publish-ghcr-images.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath, "-Target", "ech-workers")
        if (-not [string]::IsNullOrWhiteSpace($ReleaseTag)) { $args += @("-ReleaseTag", $ReleaseTag) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrOwner)) { $args += @("-GhcrOwner", $GhcrOwner) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrUsername)) { $args += @("-GhcrUsername", $GhcrUsername) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrToken)) { $args += @("-GhcrToken", $GhcrToken) }
        if ($NoCache) { $args += "-NoCache" }
        if ($LoadOnly) { $args += "-LoadOnly" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "publish ech-workers image failed"
        break
    }
    "publish-core-images" {
        $scriptPath = Join-Path $PSScriptRoot 'publish-ghcr-images.ps1'
        $args = @("-ExecutionPolicy", "Bypass", "-File", $scriptPath, "-ConfigPath", $resolvedConfigPath, "-Target", "both")
        if (-not [string]::IsNullOrWhiteSpace($ReleaseTag)) { $args += @("-ReleaseTag", $ReleaseTag) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrOwner)) { $args += @("-GhcrOwner", $GhcrOwner) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrUsername)) { $args += @("-GhcrUsername", $GhcrUsername) }
        if (-not [string]::IsNullOrWhiteSpace($GhcrToken)) { $args += @("-GhcrToken", $GhcrToken) }
        if ($NoCache) { $args += "-NoCache" }
        if ($LoadOnly) { $args += "-LoadOnly" }
        Invoke-EasyProxyExternalCommand -FilePath "powershell" -Arguments $args -FailureMessage "publish core images failed"
        break
    }
    default {
        throw "Unsupported project: $Project"
    }
}

Write-Host "Done: $Project" -ForegroundColor Green
