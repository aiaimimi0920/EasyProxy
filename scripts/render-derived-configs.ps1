param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\config.yaml'),
    [switch]$ServiceBase,
    [switch]$MiSub,
    [switch]$EchWorkersCloudflare,
    [string]$ServiceOutput = (Join-Path $PSScriptRoot '..\deploy\service\base\config.yaml'),
    [string]$MiSubEnvOutput = (Join-Path $PSScriptRoot '..\upstreams\misub\.env'),
    [string]$WorkerDevVarsOutput = (Join-Path $PSScriptRoot '..\workers\ech-workers-cloudflare\.dev.vars')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not $ServiceBase -and -not $MiSub -and -not $EchWorkersCloudflare) {
    $ServiceBase = $true
    $MiSub = $true
    $EchWorkersCloudflare = $true
}

$renderer = Join-Path $PSScriptRoot 'render-derived-configs.py'
if (-not (Test-Path -LiteralPath $renderer)) {
    throw "Missing renderer script: $renderer"
}

$args = @($renderer, '--root-config', $ConfigPath)
if ($ServiceBase) {
    $args += @('--service-output', $ServiceOutput)
}
if ($MiSub) {
    $args += @('--misub-env-output', $MiSubEnvOutput)
}
if ($EchWorkersCloudflare) {
    $args += @('--worker-devvars-output', $WorkerDevVarsOutput)
}

& python @args
if ($LASTEXITCODE -ne 0) {
    throw "Failed to render derived configs with exit code $LASTEXITCODE"
}

if ($ServiceBase) {
    Write-Host "Service config rendered: $ServiceOutput"
}
if ($MiSub) {
    Write-Host "MiSub env rendered: $MiSubEnvOutput"
}
if ($EchWorkersCloudflare) {
    Write-Host "Worker .dev.vars rendered: $WorkerDevVarsOutput"
}
