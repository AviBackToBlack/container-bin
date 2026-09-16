#Requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidateCount(2, 100)]
    [string[]]$InputPath,

    [ValidateSet('Markdown', 'Json')]
    [string]$Format = 'Markdown'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-RequiredProperty {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Object,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Source
    )

    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        throw "$Source is missing required property '$Name'."
    }
    return $property.Value
}

function Get-RequiredString {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Object,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Source
    )

    $value = Get-RequiredProperty -Object $Object -Name $Name -Source $Source
    if ($value -isnot [string] -or [string]::IsNullOrWhiteSpace($value)) {
        throw "$Source property '$Name' must be a non-empty string."
    }
    return $value
}

function Get-RequiredInteger {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Object,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Source,

        [int]$Minimum = 1
    )

    $value = Get-RequiredProperty -Object $Object -Name $Name -Source $Source
    if ($value -isnot [ValueType] -or $value -is [bool]) {
        throw "$Source property '$Name' must be an integer."
    }
    $number = [double]$value
    if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -ne [Math]::Floor($number) -or $number -lt $Minimum) {
        throw "$Source property '$Name' must be an integer greater than or equal to $Minimum."
    }
    return [int]$number
}

function Get-RequiredNumber {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Object,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Source
    )

    $value = Get-RequiredProperty -Object $Object -Name $Name -Source $Source
    if ($value -isnot [ValueType] -or $value -is [bool]) {
        throw "$Source property '$Name' must be a non-negative number."
    }
    $number = [double]$value
    if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -lt 0) {
        throw "$Source property '$Name' must be a non-negative number."
    }
    return $number
}

function Get-RequiredStringArray {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Object,

        [Parameter(Mandatory = $true)]
        [string]$Name,

        [Parameter(Mandatory = $true)]
        [string]$Source
    )

    $values = @(Get-RequiredProperty -Object $Object -Name $Name -Source $Source)
    if ($values.Count -eq 0) {
        throw "$Source property '$Name' must contain at least one string."
    }
    foreach ($value in $values) {
        if ($value -isnot [string] -or [string]::IsNullOrWhiteSpace($value)) {
            throw "$Source property '$Name' must contain only non-empty strings."
        }
    }
    return [string[]]$values
}

function Read-BenchmarkResult {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    try {
        $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath
    }
    catch {
        throw "Benchmark result does not exist: $Path"
    }
    if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        throw "Benchmark result is not a file: $resolved"
    }

    try {
        $document = [IO.File]::ReadAllText($resolved) | ConvertFrom-Json -ErrorAction Stop
    }
    catch {
        throw "Benchmark result is not valid JSON: $resolved ($($_.Exception.Message))"
    }

    $schemaVersion = Get-RequiredInteger -Object $document -Name 'schema_version' -Source $resolved
    if ($schemaVersion -ne 1) {
        throw "Unsupported benchmark schema_version $schemaVersion in $resolved; expected 1."
    }

    $generatedValue = Get-RequiredProperty -Object $document -Name 'generated_at' -Source $resolved
    $generated = [DateTimeOffset]::MinValue
    if ($generatedValue -is [DateTimeOffset]) {
        $generated = $generatedValue
    }
    elseif ($generatedValue -is [DateTime]) {
        $generated = [DateTimeOffset]$generatedValue
    }
    elseif ($generatedValue -isnot [string] -or -not [DateTimeOffset]::TryParse($generatedValue, [ref]$generated)) {
        throw "$resolved property 'generated_at' is not a valid timestamp."
    }

    $iterations = Get-RequiredInteger -Object $document -Name 'iterations' -Source $resolved
    $measurementsRaw = @(Get-RequiredProperty -Object $document -Name 'measurements' -Source $resolved)
    if ($measurementsRaw.Count -eq 0) {
        throw "$resolved property 'measurements' must not be empty."
    }

    $seen = @{}
    $measurements = @(
        foreach ($measurement in $measurementsRaw) {
            if ($null -eq $measurement -or $measurement -isnot [pscustomobject]) {
                throw "$resolved measurement must be a JSON object."
            }
            $name = Get-RequiredString -Object $measurement -Name 'name' -Source "$resolved measurement"
            if ($seen.ContainsKey($name)) {
                throw "$resolved contains duplicate measurement '$name'."
            }
            $seen[$name] = $true

            $samples = Get-RequiredInteger -Object $measurement -Name 'samples' -Source "$resolved measurement '$name'"
            if ($samples -ne $iterations) {
                throw "$resolved measurement '$name' has $samples samples but iterations is $iterations."
            }
            $minimum = Get-RequiredNumber -Object $measurement -Name 'min_ms' -Source "$resolved measurement '$name'"
            $p50 = Get-RequiredNumber -Object $measurement -Name 'p50_ms' -Source "$resolved measurement '$name'"
            $p95 = Get-RequiredNumber -Object $measurement -Name 'p95_ms' -Source "$resolved measurement '$name'"
            $maximum = Get-RequiredNumber -Object $measurement -Name 'max_ms' -Source "$resolved measurement '$name'"
            $average = Get-RequiredNumber -Object $measurement -Name 'average_ms' -Source "$resolved measurement '$name'"
            if ($p50 -le 0 -or $p95 -le 0 -or $minimum -gt $p50 -or $p50 -gt $p95 -or $p95 -gt $maximum -or $average -lt $minimum -or $average -gt $maximum) {
                throw "$resolved measurement '$name' has inconsistent latency statistics."
            }

            [pscustomobject][ordered]@{
                name       = $name
                samples    = $samples
                min_ms     = $minimum
                p50_ms     = $p50
                p95_ms     = $p95
                max_ms     = $maximum
                average_ms = $average
            }
        }
    )

    $dockerOSType = Get-RequiredString -Object $document -Name 'docker_os_type' -Source $resolved
    if ($dockerOSType -ne 'linux') {
        throw "$resolved was not captured in Docker Linux-container mode."
    }
    $imageID = Get-RequiredString -Object $document -Name 'image_id' -Source $resolved
    if ($imageID -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "$resolved property 'image_id' is not a canonical sha256 image ID."
    }

    return [pscustomobject][ordered]@{
        source             = $resolved
        generated_at       = $generated.ToUniversalTime().ToString('o')
        windows_version    = Get-RequiredString -Object $document -Name 'windows_version' -Source $resolved
        powershell_version = Get-RequiredString -Object $document -Name 'powershell_version' -Source $resolved
        docker_engine      = Get-RequiredString -Object $document -Name 'docker_engine' -Source $resolved
        docker_os_type     = $dockerOSType
        working_directory  = Get-RequiredString -Object $document -Name 'working_directory' -Source $resolved
        shim_path          = Get-RequiredString -Object $document -Name 'shim_path' -Source $resolved
        shim_arguments     = @(Get-RequiredStringArray -Object $document -Name 'shim_arguments' -Source $resolved)
        image              = Get-RequiredString -Object $document -Name 'image' -Source $resolved
        image_id           = $imageID
        container_command  = @(Get-RequiredStringArray -Object $document -Name 'container_command' -Source $resolved)
        warmups            = Get-RequiredInteger -Object $document -Name 'warmups' -Source $resolved -Minimum 0
        iterations         = $iterations
        measurements       = $measurements
    }
}

function Get-ComparisonSignature {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Result
    )

    $measurementNames = @($Result.measurements | ForEach-Object { $_.name } | Sort-Object)
    return [pscustomobject][ordered]@{
        windows_version    = $Result.windows_version
        powershell_version = $Result.powershell_version
        docker_os_type     = $Result.docker_os_type
        working_directory  = $Result.working_directory
        shim_path          = $Result.shim_path
        shim_arguments     = $Result.shim_arguments
        image_id           = $Result.image_id
        container_command  = $Result.container_command
        warmups            = $Result.warmups
        iterations         = $Result.iterations
        measurement_names  = $measurementNames
    } | ConvertTo-Json -Depth 5 -Compress
}

function Get-PercentChange {
    param(
        [Parameter(Mandatory = $true)]
        [double]$Value,

        [Parameter(Mandatory = $true)]
        [double]$Baseline
    )

    return [Math]::Round((($Value - $Baseline) / $Baseline) * 100, 2)
}

function Format-PercentChange {
    param(
        [Parameter(Mandatory = $true)]
        [double]$Value
    )

    $prefix = if ($Value -gt 0) { '+' } else { '' }
    return $prefix + $Value.ToString('0.00', [Globalization.CultureInfo]::InvariantCulture) + '%'
}

function Escape-MarkdownCell {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Value
    )

    return $Value.Replace('|', '\|').Replace("`r", ' ').Replace("`n", ' ')
}

$resolvedInputs = @{}
$results = @(
    foreach ($path in $InputPath) {
        $result = Read-BenchmarkResult -Path $path
        if ($resolvedInputs.ContainsKey($result.source)) {
            throw "Benchmark result was provided more than once: $($result.source)"
        }
        $resolvedInputs[$result.source] = $true
        $result
    }
)

$baseline = $results[0]
$baselineSignature = Get-ComparisonSignature -Result $baseline
foreach ($result in $results | Select-Object -Skip 1) {
    if ((Get-ComparisonSignature -Result $result) -ne $baselineSignature) {
        throw "Benchmark result is not comparable with baseline '$($baseline.source)': $($result.source). Windows/PowerShell, CWD, shim, arguments, image ID, sample counts, or measurement names differ."
    }
}

$baselineMeasurements = @{}
foreach ($measurement in $baseline.measurements) {
    $baselineMeasurements[$measurement.name] = $measurement
}

$reportRuns = @(
    foreach ($result in $results) {
        $reportMeasurements = @(
            foreach ($measurement in $result.measurements) {
                $baselineMeasurement = $baselineMeasurements[$measurement.name]
                [pscustomobject][ordered]@{
                    name               = $measurement.name
                    samples            = $measurement.samples
                    p50_ms             = $measurement.p50_ms
                    p50_change_percent = Get-PercentChange -Value $measurement.p50_ms -Baseline $baselineMeasurement.p50_ms
                    p95_ms             = $measurement.p95_ms
                    p95_change_percent = Get-PercentChange -Value $measurement.p95_ms -Baseline $baselineMeasurement.p95_ms
                }
            }
        )
        [pscustomobject][ordered]@{
            source        = $result.source
            generated_at  = $result.generated_at
            docker_engine = $result.docker_engine
            measurements  = $reportMeasurements
        }
    }
)

$report = [pscustomobject][ordered]@{
    schema_version = 1
    baseline       = $baseline.source
    workload       = [pscustomobject][ordered]@{
        windows_version    = $baseline.windows_version
        powershell_version = $baseline.powershell_version
        working_directory  = $baseline.working_directory
        shim_path          = $baseline.shim_path
        shim_arguments     = $baseline.shim_arguments
        image              = $baseline.image
        image_id           = $baseline.image_id
        container_command  = $baseline.container_command
        warmups            = $baseline.warmups
        iterations         = $baseline.iterations
    }
    runs           = $reportRuns
}

if ($Format -eq 'Json') {
    $report | ConvertTo-Json -Depth 8
    return
}

$lines = @(
    "# ContainerBin startup benchmark comparison"
    ""
    "Baseline file: $(Escape-MarkdownCell -Value $baseline.source)"
    ""
    "Workload: ``$(Escape-MarkdownCell -Value $baseline.image)`` (``$(Escape-MarkdownCell -Value $baseline.image_id)``), $($baseline.iterations) measured iterations after $($baseline.warmups) warmups."
    ""
    '| Docker Engine | Generated (UTC) | Measurement | p50 (ms) | p50 change | p95 (ms) | p95 change |'
    '|---|---|---|---:|---:|---:|---:|'
)
foreach ($run in $report.runs) {
    foreach ($measurement in $run.measurements) {
        $lines += '| {0} | {1} | {2} | {3} | {4} | {5} | {6} |' -f @(
            (Escape-MarkdownCell -Value $run.docker_engine),
            (Escape-MarkdownCell -Value $run.generated_at),
            (Escape-MarkdownCell -Value $measurement.name),
            $measurement.p50_ms.ToString('0.00', [Globalization.CultureInfo]::InvariantCulture),
            (Format-PercentChange -Value $measurement.p50_change_percent),
            $measurement.p95_ms.ToString('0.00', [Globalization.CultureInfo]::InvariantCulture),
            (Format-PercentChange -Value $measurement.p95_change_percent)
        )
    }
}

$lines -join [Environment]::NewLine
