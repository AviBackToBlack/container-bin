#Requires -Version 5.1

[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-True {
    param(
        [Parameter(Mandatory = $true)]
        [bool]$Condition,

        [Parameter(Mandatory = $true)]
        [string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function New-Fixture {
    param(
        [Parameter(Mandatory = $true)]
        [string]$DockerEngine,

        [Parameter(Mandatory = $true)]
        [double]$Scale,

        [string]$ImageID = ('sha256:' + ('a' * 64)),

        [int]$SchemaVersion = 1
    )

    $measurements = @(
        [pscustomobject][ordered]@{
            name       = 'container-bin-shim'
            samples    = 5
            min_ms     = 80 * $Scale
            p50_ms     = 100 * $Scale
            p95_ms     = 120 * $Scale
            max_ms     = 130 * $Scale
            average_ms = 105 * $Scale
        }
        [pscustomobject][ordered]@{
            name       = 'docker-run-default-pull'
            samples    = 5
            min_ms     = 40 * $Scale
            p50_ms     = 50 * $Scale
            p95_ms     = 60 * $Scale
            max_ms     = 70 * $Scale
            average_ms = 55 * $Scale
        }
    )

    return [pscustomobject][ordered]@{
        schema_version     = $SchemaVersion
        generated_at       = '2026-09-16T12:00:00Z'
        windows_version    = 'Microsoft Windows NT 10.0.26200.0'
        powershell_version = '7.6.6'
        docker_engine      = $DockerEngine
        docker_os_type     = 'linux'
        working_directory  = 'D:\Work\demo'
        shim_path          = 'D:\Tools\ContainerBin\node22.exe'
        shim_arguments     = @('--version')
        image              = 'node:22-slim'
        image_id           = $ImageID
        container_command  = @('node', '--version')
        warmups            = 3
        iterations         = 5
        measurements       = $measurements
    }
}

$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$testDirectory = [IO.Path]::GetFullPath((Join-Path $tempBase ('container-bin-benchmark-tests-' + [Guid]::NewGuid().ToString('N'))))
if (-not $testDirectory.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to use test directory outside the system temp directory: $testDirectory"
}
$compareScript = Join-Path $PSScriptRoot 'compare-startup-benchmarks.ps1'

try {
    [IO.Directory]::CreateDirectory($testDirectory) | Out-Null
    $baselinePath = Join-Path $testDirectory 'baseline.json'
    $candidatePath = Join-Path $testDirectory 'candidate.json'
    $mismatchPath = Join-Path $testDirectory 'mismatch.json'
    $schemaPath = Join-Path $testDirectory 'schema.json'

    [IO.File]::WriteAllText($baselinePath, ((New-Fixture -DockerEngine '29.7.2' -Scale 1.0) | ConvertTo-Json -Depth 6))
    [IO.File]::WriteAllText($candidatePath, ((New-Fixture -DockerEngine '29.8.0' -Scale 1.1) | ConvertTo-Json -Depth 6))
    [IO.File]::WriteAllText($mismatchPath, ((New-Fixture -DockerEngine '29.8.0' -Scale 1.1 -ImageID ('sha256:' + ('b' * 64))) | ConvertTo-Json -Depth 6))
    [IO.File]::WriteAllText($schemaPath, ((New-Fixture -DockerEngine '29.8.0' -Scale 1.1 -SchemaVersion 2) | ConvertTo-Json -Depth 6))

    $jsonText = @(& $compareScript -InputPath @($baselinePath, $candidatePath) -Format Json) -join [Environment]::NewLine
    $report = $jsonText | ConvertFrom-Json -ErrorAction Stop
    Assert-True -Condition ($report.schema_version -eq 1) -Message 'comparison report schema version mismatch'
    Assert-True -Condition ($jsonText -match '"shim_arguments"\s*:\s*\[') -Message 'comparison report did not preserve shim_arguments as an array'
    Assert-True -Condition ($jsonText -match '"container_command"\s*:\s*\[') -Message 'comparison report did not preserve container_command as an array'
    Assert-True -Condition (@($report.runs).Count -eq 2) -Message 'comparison report run count mismatch'
    Assert-True -Condition ($report.runs[1].docker_engine -eq '29.8.0') -Message 'candidate Docker version missing'
    Assert-True -Condition ($report.runs[1].measurements[0].p50_change_percent -eq 10) -Message 'p50 change was not calculated against baseline'
    Assert-True -Condition ($report.runs[1].measurements[0].p95_change_percent -eq 10) -Message 'p95 change was not calculated against baseline'

    $markdown = @(& $compareScript -InputPath @($baselinePath, $candidatePath) -Format Markdown) -join [Environment]::NewLine
    Assert-True -Condition ($markdown.Contains('| 29.7.2 |')) -Message 'baseline missing from Markdown output'
    Assert-True -Condition ($markdown.Contains('| 29.8.0 |')) -Message 'candidate missing from Markdown output'
    Assert-True -Condition ($markdown.Contains('+10.00%')) -Message 'Markdown output missing positive change'

    $mismatchFailed = $false
    try {
        & $compareScript -InputPath @($baselinePath, $mismatchPath) -Format Json *> $null
    }
    catch {
        $mismatchFailed = $_.Exception.Message -match 'not comparable with baseline'
    }
    Assert-True -Condition $mismatchFailed -Message 'incompatible image IDs were not rejected'

    $schemaFailed = $false
    try {
        & $compareScript -InputPath @($baselinePath, $schemaPath) -Format Json *> $null
    }
    catch {
        $schemaFailed = $_.Exception.Message -match 'Unsupported benchmark schema_version 2'
    }
    Assert-True -Condition $schemaFailed -Message 'unsupported input schema was not rejected'
}
finally {
    if (Test-Path -LiteralPath $testDirectory) {
        $resolvedTestDirectory = [IO.Path]::GetFullPath((Resolve-Path -LiteralPath $testDirectory).ProviderPath)
        if (-not $resolvedTestDirectory.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove test directory outside the system temp directory: $resolvedTestDirectory"
        }
        Remove-Item -LiteralPath $resolvedTestDirectory -Recurse -Force
    }
}

Write-Output 'compare-startup-benchmarks tests passed'
