#Requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ShimPath,

    [string[]]$ShimArguments = @('--version'),

    [string]$Image = 'node:22-slim',

    [string[]]$ContainerCommand = @('node', '--version'),

    [ValidateRange(3, 1000)]
    [int]$Iterations = 15,

    [ValidateRange(0, 100)]
    [int]$Warmups = 3
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-NativeQuiet {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,

        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    & $FilePath @Arguments *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath exited $LASTEXITCODE while running: $($Arguments -join ' ')"
    }
}

function Get-Percentile {
    param(
        [Parameter(Mandatory = $true)]
        [double[]]$SortedSamples,

        [Parameter(Mandatory = $true)]
        [ValidateRange(0, 1)]
        [double]$Percentile
    )

    $index = [Math]::Ceiling($SortedSamples.Count * $Percentile) - 1
    if ($index -lt 0) {
        $index = 0
    }
    return $SortedSamples[$index]
}

function Measure-NativeSeries {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$FilePath,

        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    for ($i = 0; $i -lt $Warmups; $i++) {
        Invoke-NativeQuiet -FilePath $FilePath -Arguments $Arguments
    }

    [double[]]$samples = @(
        for ($i = 0; $i -lt $Iterations; $i++) {
            $watch = [Diagnostics.Stopwatch]::StartNew()
            Invoke-NativeQuiet -FilePath $FilePath -Arguments $Arguments
            $watch.Stop()
            $watch.Elapsed.TotalMilliseconds
        }
    )
    [Array]::Sort($samples)

    [pscustomobject][ordered]@{
        name       = $Name
        samples    = $samples.Count
        min_ms     = [Math]::Round($samples[0], 2)
        p50_ms     = [Math]::Round((Get-Percentile -SortedSamples $samples -Percentile 0.50), 2)
        p95_ms     = [Math]::Round((Get-Percentile -SortedSamples $samples -Percentile 0.95), 2)
        max_ms     = [Math]::Round($samples[-1], 2)
        average_ms = [Math]::Round(($samples | Measure-Object -Average).Average, 2)
    }
}

if (-not [IO.Path]::IsPathRooted($ShimPath)) {
    throw 'ShimPath must be an absolute Windows path.'
}
$resolvedShim = [IO.Path]::GetFullPath($ShimPath)
if (-not (Test-Path -LiteralPath $resolvedShim -PathType Leaf)) {
    throw "ShimPath does not exist: $resolvedShim"
}
if ($ShimArguments.Count -eq 0) {
    throw 'ShimArguments must contain at least one non-interactive argument.'
}
if ($ContainerCommand.Count -eq 0) {
    throw 'ContainerCommand must contain at least one argument.'
}

$dockerCommand = Get-Command docker -CommandType Application -ErrorAction Stop | Select-Object -First 1
$dockerPath = $dockerCommand.Source
$dockerVersion = (& $dockerPath version --format '{{.Server.Version}}').Trim()
if ($LASTEXITCODE -ne 0 -or $dockerVersion.Length -eq 0) {
    throw 'Docker Engine is unavailable.'
}
$dockerOSType = (& $dockerPath info --format '{{.OSType}}').Trim()
if ($LASTEXITCODE -ne 0 -or $dockerOSType -ne 'linux') {
    throw "Docker must be available in Linux-container mode; detected OSType: $dockerOSType"
}
$imageID = (& $dockerPath image inspect $Image --format '{{.Id}}' 2>$null).Trim()
if ($LASTEXITCODE -ne 0 -or $imageID -notmatch '^sha256:[0-9a-f]{64}$') {
    throw "Image must already exist locally; refusing to pull: $Image"
}

$containerName = 'cb-rm28-benchmark-' + [Guid]::NewGuid().ToString('N')
$containerStarted = $false
try {
    $startArgs = @(
        'run', '--detach', '--rm',
        '--name', $containerName,
        '--label', 'cb.benchmark=rm28',
        $imageID,
        'sh', '-c', 'while :; do sleep 3600; done'
    )
    Invoke-NativeQuiet -FilePath $dockerPath -Arguments $startArgs
    $containerStarted = $true

    $measurements = @(
        Measure-NativeSeries -Name 'container-bin-shim' -FilePath $resolvedShim -Arguments $ShimArguments
        Measure-NativeSeries -Name 'docker-run-default-pull' -FilePath $dockerPath -Arguments (@('run', '--rm', $imageID) + $ContainerCommand)
        Measure-NativeSeries -Name 'docker-run-pull-never' -FilePath $dockerPath -Arguments (@('run', '--rm', '--pull=never', $imageID) + $ContainerCommand)
        Measure-NativeSeries -Name 'docker-run-network-none' -FilePath $dockerPath -Arguments (@('run', '--rm', '--pull=never', '--network', 'none', $imageID) + $ContainerCommand)
        Measure-NativeSeries -Name 'docker-exec-control' -FilePath $dockerPath -Arguments (@('exec', $containerName) + $ContainerCommand)
    )

    [pscustomobject][ordered]@{
        schema_version     = 1
        generated_at       = [DateTime]::UtcNow.ToString('o')
        windows_version    = [Environment]::OSVersion.VersionString
        powershell_version = $PSVersionTable.PSVersion.ToString()
        docker_engine      = $dockerVersion
        docker_os_type     = $dockerOSType
        working_directory  = (Get-Location).Path
        shim_path          = $resolvedShim
        shim_arguments     = $ShimArguments
        image              = $Image
        image_id           = $imageID
        container_command  = $ContainerCommand
        warmups            = $Warmups
        iterations         = $Iterations
        measurements       = $measurements
    } | ConvertTo-Json -Depth 6
}
finally {
    if ($containerStarted) {
        & $dockerPath rm --force $containerName *> $null
        if ($LASTEXITCODE -ne 0) {
            throw "Could not remove benchmark container $containerName"
        }
    }
}
