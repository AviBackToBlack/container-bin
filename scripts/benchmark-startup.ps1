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

function Test-DockerContainerExists {
    param(
        [Parameter(Mandatory = $true)]
        [string]$DockerPath,

        [Parameter(Mandatory = $true)]
        [string]$ContainerName
    )

    $inspectOutput = @(& $DockerPath container inspect --format '{{.Id}}' $ContainerName 2>&1)
    $inspectExitCode = $LASTEXITCODE
    if ($inspectExitCode -eq 0) {
        return $true
    }

    $inspectMessage = ($inspectOutput -join [Environment]::NewLine).Trim()
    if ($inspectMessage -match '(?i)no such (object|container)') {
        return $false
    }
    throw "Could not verify benchmark container cleanup: $inspectMessage"
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

$isDriveAbsolute = $ShimPath -match '^[A-Za-z]:[\\/]'
$isUNC = $ShimPath -match '^\\\\(?![?.]\\)[^\\]+\\[^\\]+'
if (-not ($isDriveAbsolute -or $isUNC)) {
    throw 'ShimPath must be a fully qualified Windows drive or UNC path.'
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
$dockerVersionOutput = @(& $dockerPath version --format '{{.Server.Version}}' 2>$null)
$dockerVersionExitCode = $LASTEXITCODE
$dockerVersion = ($dockerVersionOutput -join [Environment]::NewLine).Trim()
if ($dockerVersionExitCode -ne 0 -or $dockerVersion.Length -eq 0) {
    throw 'Docker Engine is unavailable.'
}
$dockerOSTypeOutput = @(& $dockerPath info --format '{{.OSType}}' 2>$null)
$dockerOSTypeExitCode = $LASTEXITCODE
$dockerOSType = ($dockerOSTypeOutput -join [Environment]::NewLine).Trim()
if ($dockerOSTypeExitCode -ne 0 -or $dockerOSType -ne 'linux') {
    throw "Docker must be available in Linux-container mode; detected OSType: $dockerOSType"
}
$imageIDOutput = @(& $dockerPath image inspect $Image --format '{{.Id}}' 2>$null)
$imageIDExitCode = $LASTEXITCODE
$imageID = ($imageIDOutput -join [Environment]::NewLine).Trim()
if ($imageIDExitCode -ne 0 -or $imageID -notmatch '^sha256:[0-9a-f]{64}$') {
    throw "Image must already exist locally; refusing to pull: $Image"
}

$containerName = 'cb-rm28-benchmark-' + [Guid]::NewGuid().ToString('N')
$containerStartAttempted = $false
$primaryError = $null
$cleanupError = $null
$resultJson = $null
try {
    $startArgs = @(
        'run', '--detach', '--rm',
        '--name', $containerName,
        '--label', 'cb.benchmark=rm28',
        $imageID,
        'sh', '-c', 'while :; do sleep 3600; done'
    )
    $containerStartAttempted = $true
    Invoke-NativeQuiet -FilePath $dockerPath -Arguments $startArgs

    $measurements = @(
        Measure-NativeSeries -Name 'container-bin-shim' -FilePath $resolvedShim -Arguments $ShimArguments
        Measure-NativeSeries -Name 'docker-run-default-pull' -FilePath $dockerPath -Arguments (@('run', '--rm', $imageID) + $ContainerCommand)
        Measure-NativeSeries -Name 'docker-run-pull-never' -FilePath $dockerPath -Arguments (@('run', '--rm', '--pull=never', $imageID) + $ContainerCommand)
        Measure-NativeSeries -Name 'docker-run-network-none' -FilePath $dockerPath -Arguments (@('run', '--rm', '--pull=never', '--network', 'none', $imageID) + $ContainerCommand)
        Measure-NativeSeries -Name 'docker-exec-control' -FilePath $dockerPath -Arguments (@('exec', $containerName) + $ContainerCommand)
    )

    $resultJson = [pscustomobject][ordered]@{
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
catch {
    $primaryError = $_
}
finally {
    if ($containerStartAttempted) {
        try {
            $removeOutput = @(& $dockerPath rm --force $containerName 2>&1)
            $removeExitCode = $LASTEXITCODE
            $removeMessage = ($removeOutput -join [Environment]::NewLine).Trim()
            $notFound = $removeMessage -match '(?i)no such (object|container)'

            if (Test-DockerContainerExists -DockerPath $dockerPath -ContainerName $containerName) {
                throw "Benchmark container still exists after cleanup: $containerName"
            }
            if ($removeExitCode -ne 0 -and -not $notFound) {
                throw "Could not remove benchmark container ${containerName}: $removeMessage"
            }
        }
        catch {
            $cleanupError = $_
        }
    }
}

if ($null -ne $primaryError) {
    if ($null -ne $cleanupError) {
        throw "$($primaryError.Exception.Message) Cleanup also failed: $($cleanupError.Exception.Message)"
    }
    throw $primaryError
}
if ($null -ne $cleanupError) {
    throw $cleanupError
}
$global:LASTEXITCODE = 0
$resultJson
