param(
    [ValidateSet(
        "easyproxy",
        "misub-pages",
        "misub-docker",
        "aggregator",
        "ech-workers-cloudflare",
        "build-easyproxy-image",
        "build-ech-workers-image"
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
    [switch]$Push
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

function Warn-WeakDefaults {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Config
    )

    $warnings = @()

    $serviceBase = Get-EasyProxyConfigSection -Config $Config -Name 'serviceBase'
    $runtime = Get-EasyProxyConfigSection -Config $serviceBase -Name 'runtime'
    $sourceSync = Get-EasyProxyConfigSection -Config $runtime -Name 'source_sync'
    $manifestUrl = [string](Get-EasyProxyConfigValue -Object $sourceSync -Name 'manifest_url' -Default '')
    if ($manifestUrl -match 'example\.com') {
        $warnings += "serviceBase.runtime.source_sync.manifest_url still uses an example domain."
    }

    $misub = Get-EasyProxyConfigSection -Config $Config -Name 'misub'
    $docker = Get-EasyProxyConfigSection -Config $misub -Name 'docker'
    $env = Get-EasyProxyConfigSection -Config $docker -Name 'env'
    $adminPassword = [string](Get-EasyProxyConfigValue -Object $env -Name 'ADMIN_PASSWORD' -Default '')
    if ($adminPassword -like 'change_me*') {
        $warnings += "misub.docker.env.ADMIN_PASSWORD still uses a placeholder value."
    }

    $worker = Get-EasyProxyConfigSection -Config $Config -Name 'echWorkersCloudflare'
    $secrets = Get-EasyProxyConfigSection -Config $worker -Name 'secrets'
    $echToken = [string](Get-EasyProxyConfigValue -Object $secrets -Name 'ECH_TOKEN' -Default '')
    if ([string]::IsNullOrWhiteSpace($echToken)) {
        $warnings += "echWorkersCloudflare.secrets.ECH_TOKEN is empty."
    }

    if ($warnings.Count -gt 0) {
        Write-Host "Config warnings:" -ForegroundColor Yellow
        foreach ($item in $warnings) {
            Write-Host (" - {0}" -f $item) -ForegroundColor Yellow
        }
    }
}

if ([string]::IsNullOrWhiteSpace($Project)) {
    throw "Missing -Project. Supported values: easyproxy, misub-pages, misub-docker, aggregator, ech-workers-cloudflare, build-easyproxy-image, build-ech-workers-image"
}

$resolvedConfigPath = Resolve-ConfigPath -Path $ConfigPath
Ensure-ConfigReady -Path $resolvedConfigPath -InitIfMissing:$InitConfig

$config = Read-EasyProxyConfig -ConfigPath $resolvedConfigPath
Warn-WeakDefaults -Config $config

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
    default {
        throw "Unsupported project: $Project"
    }
}

Write-Host "Done: $Project" -ForegroundColor Green
