#Requires -Version 7.0

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$')]
    [string]$FromVersion,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$')]
    [string]$TargetVersion,

    [Parameter(Mandatory = $true)]
    [string]$GitHubCLI,

    [string]$GoExecutable
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-ExactProcess {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Executable,

        [Parameter(Mandatory = $true)]
        [string[]]$Arguments,

        [Parameter(Mandatory = $true)]
        [string]$WorkingDirectory,

        [hashtable]$Environment = @{}
    )

    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $Executable
    $start.WorkingDirectory = $WorkingDirectory
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) {
        [void]$start.ArgumentList.Add($argument)
    }
    foreach ($name in $Environment.Keys) {
        $start.Environment[$name] = [string]$Environment[$name]
    }

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        if (-not $process.Start()) {
            throw "Failed to start $Executable"
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        return [pscustomobject]@{
            ExitCode = $process.ExitCode
            Stdout   = $stdout
            Stderr   = $stderr
        }
    }
    finally {
        $process.Dispose()
    }
}

function Assert-Succeeded {
    param(
        [Parameter(Mandatory = $true)]
        [pscustomobject]$Result,

        [Parameter(Mandatory = $true)]
        [string]$Operation
    )

    if ($Result.ExitCode -ne 0) {
        throw "$Operation failed with exit code $($Result.ExitCode). stdout=$($Result.Stdout.Trim()) stderr=$($Result.Stderr.Trim())"
    }
}

if (-not $IsWindows) {
    throw 'Published self-update qualification requires native Windows.'
}
if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne [Runtime.InteropServices.Architecture]::X64) {
    throw 'This qualification harness currently covers Windows/amd64 only.'
}
if ($FromVersion -eq $TargetVersion) {
    throw 'FromVersion and TargetVersion must differ.'
}
if ([string]::IsNullOrWhiteSpace($env:GH_TOKEN) -and [string]::IsNullOrWhiteSpace($env:GITHUB_TOKEN)) {
    throw 'Set an explicit GH_TOKEN or GITHUB_TOKEN for GitHub attestation verification.'
}

$repositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if (-not (Test-Path -LiteralPath (Join-Path $repositoryRoot 'go.mod') -PathType Leaf)) {
    throw "Repository root is not valid: $repositoryRoot"
}

if ([string]::IsNullOrWhiteSpace($GoExecutable)) {
    $GoExecutable = (Get-Command go.exe -ErrorAction Stop).Source
}
foreach ($executable in @($GoExecutable, $GitHubCLI)) {
    if (-not [IO.Path]::IsPathFullyQualified($executable)) {
        throw "Required executable path must be absolute: $executable"
    }
    if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
        throw "Required executable does not exist: $executable"
    }
}
$GoExecutable = [IO.Path]::GetFullPath($GoExecutable)
$GitHubCLI = [IO.Path]::GetFullPath($GitHubCLI)

$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
$qualificationRoot = [IO.Path]::GetFullPath((Join-Path $tempRoot ('container-bin-self-update-e2e-' + [Guid]::NewGuid().ToString('N'))))
if (-not [string]::Equals([IO.Path]::GetDirectoryName($qualificationRoot), $tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
    -not [IO.Path]::GetFileName($qualificationRoot).StartsWith('container-bin-self-update-e2e-', [StringComparison]::Ordinal)) {
    throw "Refusing to use qualification directory outside the system temp directory: $qualificationRoot"
}

[IO.Directory]::CreateDirectory($qualificationRoot) | Out-Null
$installed = Join-Path $qualificationRoot 'cb.exe'
$hardlinkShim = Join-Path $qualificationRoot 'go.exe'
$copyShim = Join-Path $qualificationRoot 'gofmt.exe'

try {
    $build = Invoke-ExactProcess `
        -Executable $GoExecutable `
        -Arguments @('build', '-trimpath', '-buildvcs=false', '-ldflags', "-s -w -X main.version=$FromVersion", '-o', $installed, '.') `
        -WorkingDirectory $repositoryRoot `
        -Environment @{ GOOS = 'windows'; GOARCH = 'amd64' }
    Assert-Succeeded -Result $build -Operation 'Build qualification executable'

    New-Item -ItemType HardLink -Path $hardlinkShim -Target $installed | Out-Null
    Copy-Item -LiteralPath $installed -Destination $copyShim

    $before = Invoke-ExactProcess -Executable $installed -Arguments @('version') -WorkingDirectory $qualificationRoot
    Assert-Succeeded -Result $before -Operation 'Read pre-update version'
    if ($before.Stdout.Trim() -ne "container-bin $FromVersion") {
        throw "Unexpected pre-update version output: $($before.Stdout.Trim())"
    }

    $apply = Invoke-ExactProcess `
        -Executable $installed `
        -Arguments @('self-update', '--apply', '--version', $TargetVersion, '--gh-executable', $GitHubCLI) `
        -WorkingDirectory $qualificationRoot
    Assert-Succeeded -Result $apply -Operation 'Apply published self-update'

    $deadline = [DateTime]::UtcNow.AddMinutes(2)
    $after = $null
    do {
        Start-Sleep -Milliseconds 250
        try {
            $candidate = Invoke-ExactProcess -Executable $installed -Arguments @('version') -WorkingDirectory $qualificationRoot
            if ($candidate.ExitCode -eq 0) {
                $after = $candidate
            }
        }
        catch {
            $after = $null
        }
    } while (($null -eq $after -or $after.Stdout.Trim() -ne "container-bin $TargetVersion") -and [DateTime]::UtcNow -lt $deadline)
    if ($null -eq $after -or $after.Stdout.Trim() -ne "container-bin $TargetVersion") {
        $lastOutput = if ($null -eq $after) { '<unavailable>' } else { $after.Stdout.Trim() }
        throw "Updated executable did not report $TargetVersion; last output: $lastOutput"
    }

    $deadline = [DateTime]::UtcNow.AddMinutes(1)
    do {
        $privateArtifacts = @(Get-ChildItem -LiteralPath $qualificationRoot -Force | Where-Object { $_.Name -like '.container-bin-update-*' })
        if ($privateArtifacts.Count -eq 0) {
            break
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($privateArtifacts.Count -ne 0) {
        throw "Private self-update artifacts remain: $($privateArtifacts.Name -join ', ')"
    }

    $installedHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash
    foreach ($shim in @($hardlinkShim, $copyShim)) {
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $shim).Hash -ne $installedHash) {
            throw "Managed shim bytes were not reconciled: $shim"
        }
    }

    $fsutil = Join-Path $env:SystemRoot 'System32\fsutil.exe'
    $links = Invoke-ExactProcess -Executable $fsutil -Arguments @('hardlink', 'list', $installed) -WorkingDirectory $qualificationRoot
    Assert-Succeeded -Result $links -Operation 'Inspect managed hardlinks'
    $hardlinkPaths = @($links.Stdout -split "`r?`n")
    foreach ($leaf in @('go.exe', 'gofmt.exe')) {
        if (-not ($hardlinkPaths | Where-Object { $_.Trim().EndsWith('\' + $leaf, [StringComparison]::OrdinalIgnoreCase) })) {
            throw "Updated managed shim is not a hardlink to cb.exe: $leaf"
        }
    }

    [pscustomobject][ordered]@{
        schema_version    = 1
        result            = 'PASS'
        source_version    = $FromVersion
        target_version    = $TargetVersion
        platform          = 'windows/amd64'
        managed_shims     = 2
        private_artifacts = 0
        apply_output      = $apply.Stdout.Trim()
    } | ConvertTo-Json -Depth 3
}
finally {
    if (Test-Path -LiteralPath $qualificationRoot) {
        $resolved = [IO.Path]::GetFullPath((Resolve-Path -LiteralPath $qualificationRoot).ProviderPath)
        if (-not [string]::Equals([IO.Path]::GetDirectoryName($resolved), $tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolved).StartsWith('container-bin-self-update-e2e-', [StringComparison]::Ordinal)) {
            throw "Refusing to remove unsafe qualification directory: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
