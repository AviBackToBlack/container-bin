#Requires -Version 5.1
# Dot-source only. No Docker operation, observation or file write runs on import.
# Evidence returned by capture helpers is PRIVATE; never send it to CI artifacts.
Set-StrictMode -Version Latest

function Get-QPSha256 {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Text)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $encoding = New-Object Text.UTF8Encoding($false, $true)
        return ([BitConverter]::ToString($sha.ComputeHash($encoding.GetBytes($Text)))).Replace('-', '').ToLowerInvariant()
    } finally { $sha.Dispose() }
}

function ConvertTo-QPJsonString {
    param([AllowEmptyString()][string]$Value)
    $builder = New-Object Text.StringBuilder
    [void]$builder.Append('"')
    foreach ($c in $Value.ToCharArray()) {
        switch ([int]$c) {
            34 { [void]$builder.Append('\"') }
            92 { [void]$builder.Append('\\') }
            default {
                if ([int]$c -lt 32) { [void]$builder.Append(('\u{0:x4}' -f [int]$c)) }
                else { [void]$builder.Append($c) }
            }
        }
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function ConvertTo-QPCanonicalJson {
    param([AllowNull()]$Value)
    if ($null -eq $Value) { return 'null' }
    if ($Value -is [string]) { return ConvertTo-QPJsonString $Value }
    if ($Value -is [bool]) { return $Value.ToString().ToLowerInvariant() }
    if ($Value -is [datetime] -or $Value -is [datetimeoffset]) { throw 'STOP: timestamp converted to a date object; retain the raw JSON string' }
    if ($Value -is [System.Collections.IDictionary] -or $Value -is [pscustomobject]) {
        $map = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
        if ($Value -is [System.Collections.IDictionary]) {
            foreach ($key in $Value.Keys) {
                if ($key -isnot [string]) { throw 'STOP: JSON key must be a string' }
                $map[$key] = $Value[$key]
            }
        } else { foreach ($p in $Value.PSObject.Properties) { $map[$p.Name] = $p.Value } }
        [string[]]$keys = @($map.Keys)
        [Array]::Sort($keys, [StringComparer]::Ordinal)
        $parts = foreach ($key in $keys) { (ConvertTo-QPJsonString $key) + ':' + (ConvertTo-QPCanonicalJson $map[$key]) }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IEnumerable]) {
        $parts = foreach ($item in $Value) { ConvertTo-QPCanonicalJson $item }
        return '[' + ($parts -join ',') + ']'
    }
    if ($Value -is [byte] -or $Value -is [int16] -or $Value -is [int32] -or $Value -is [int64] -or
        $Value -is [uint16] -or $Value -is [uint32] -or $Value -is [uint64] -or $Value -is [decimal] -or
        $Value -is [double] -or $Value -is [single]) {
        # The v1 canonical schema supports integral Int64 values only. Preserve
        # other numbers in raw sources; never hash edition-dependent float text.
        if ($Value -is [double] -or $Value -is [single]) {
            $limit = if ($Value -is [single]) { 16777215 } else { 9007199254740991 }
            if ([double]::IsNaN($Value) -or [double]::IsInfinity($Value) -or [Math]::Abs([double]$Value) -gt $limit) { throw 'STOP: ambiguous floating-point evidence number' }
        }
        if ($Value -lt [long]::MinValue -or $Value -gt [long]::MaxValue -or [decimal]::Truncate([decimal]$Value) -ne $Value) { throw 'STOP: canonical evidence requires integral Int64 numbers' }
        return ([long]$Value).ToString([Globalization.CultureInfo]::InvariantCulture)
    }
    throw 'STOP: unsupported evidence value type'
}

function ConvertFrom-QPJsonElement {
    param($Element)
    switch ($Element.ValueKind.ToString()) {
        'Object' {
            $map = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
            foreach ($p in $Element.EnumerateObject()) {
                if ($map.ContainsKey($p.Name)) { throw 'STOP: duplicate JSON key' }
                $map.Add($p.Name, (ConvertFrom-QPJsonElement $p.Value))
            }
            return ,$map
        }
        'Array' { return ,@($Element.EnumerateArray() | ForEach-Object { ConvertFrom-QPJsonElement $_ }) }
        'String' { return $Element.GetString() }
        'Number' { return $Element.GetDecimal() }
        'True' { return $true }
        'False' { return $false }
        'Null' { return $null }
        default { throw 'STOP: unsupported JSON token' }
    }
}

function Read-QPJson {
    param([Parameter(Mandatory = $true)][string]$Raw)
    # ConvertFrom-Json historically turns ISO labels into DateTime, changing bytes
    # on serialization. Both parsers below preserve timestamp strings verbatim.
    if ($PSVersionTable.PSEdition -eq 'Desktop') {
        Add-Type -AssemblyName System.Web.Extensions
        $parser = New-Object System.Web.Script.Serialization.JavaScriptSerializer
        $parser.MaxJsonLength = 16777216
        # JavaScriptSerializer silently overwrites duplicate keys. Check decoded
        # key tokens first, including differently escaped spellings of a key.
        $tokens = [regex]::Matches($Raw, '"(?:\\.|[^"\\])*"|[{}\[\]:,]|[^\s{}\[\]:,"]+')
        $objects = New-Object 'System.Collections.Generic.Stack[object]'
        for ($i = 0; $i -lt $tokens.Count; $i++) {
            $token = $tokens[$i].Value
            if ($token -ceq '{') { $objects.Push((New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal))) }
            elseif ($token -ceq '}') { [void]$objects.Pop() }
            elseif ($token.StartsWith('"') -and $i + 1 -lt $tokens.Count -and $tokens[$i + 1].Value -ceq ':') {
                $key = $parser.DeserializeObject($token)
                if ($objects.Count -eq 0 -or -not $objects.Peek().Add($key)) { throw 'STOP: duplicate/invalid JSON key' }
            }
        }
        return ,$parser.DeserializeObject($Raw)
    }
    $document = [System.Text.Json.JsonDocument]::Parse($Raw)
    try { return ,(ConvertFrom-QPJsonElement $document.RootElement) }
    finally { $document.Dispose() }
}

function Assert-QPEqual {
    param($Expected, $Actual, [string]$Area)
    if ((ConvertTo-QPCanonicalJson $Expected) -cne (ConvertTo-QPCanonicalJson $Actual)) {
        throw "STOP: $Area differs"
    }
}

function Assert-QPFields {
    param($Map, [string[]]$Names)
    if ($Map -isnot [System.Collections.IDictionary]) { throw 'STOP: evidence object required' }
    foreach ($name in $Names) {
        if (@($Map.Keys) -cnotcontains $name -or $null -eq $Map[$name]) { throw "STOP: missing evidence field $name" }
    }
}

function New-QPPathValue {
    param([AllowNull()]$Raw, [bool]$Present, [string]$RegistryKind)
    if ($Present -and ($Raw -isnot [string] -or $RegistryKind -notin @('process', 'String', 'ExpandString'))) { throw 'STOP: non-string/unsupported PATH value' }
    if (-not $Present -and -not [string]::IsNullOrEmpty($Raw)) { throw 'STOP: absent PATH has a value' }
    return [ordered]@{
        present = $Present; raw = $(if ($Present) { $Raw } else { $null }); encoding = 'UTF-8/no-BOM/strict'
        sha256 = $(if ($Present) { Get-QPSha256 $Raw } else { $null }); registry_kind = $RegistryKind
        segments = @($(if ($Present) { $Raw.Split([char]';', [StringSplitOptions]::None) }))
    }
}

function Assert-QPUtcTimestamp {
    param($Value)
    $parsed = [DateTimeOffset]::MinValue
    if ($Value -isnot [string] -or $Value -cnotmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,7})?(?:Z|\+00:00)$' -or
        -not [DateTimeOffset]::TryParse($Value, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None, [ref]$parsed)) { throw 'STOP: missing/invalid UTC provenance' }
}

function Get-QPPathSnapshot {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$ProfileMode,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Launcher,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$ChildOverlays
    )
    # Caller must supply the actual launch recipe; ancestry cannot reconstruct it.
    if ([string]::IsNullOrWhiteSpace($ProfileMode) -or $Launcher.Count -eq 0) { throw 'STOP: missing launch provenance' }
    $process = [Diagnostics.Process]::GetCurrentProcess()
    $cim = Get-CimInstance Win32_Process -Filter "ProcessId=$PID" -ErrorAction Stop
    $parent = Get-CimInstance Win32_Process -Filter "ProcessId=$($cim.ParentProcessId)" -ErrorAction Stop
    if ($null -eq $parent -or [string]::IsNullOrWhiteSpace($parent.ExecutablePath)) { throw 'STOP: missing parent provenance' }
    $scopes = [ordered]@{}
    $scopes.process = New-QPPathValue -Raw ([Environment]::GetEnvironmentVariable('PATH', 'Process')) -Present ($null -ne [Environment]::GetEnvironmentVariable('PATH', 'Process')) -RegistryKind 'process'
    foreach ($scope in @('user', 'machine')) {
        $key = if ($scope -eq 'user') { [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment') }
        else { [Microsoft.Win32.Registry]::LocalMachine.OpenSubKey('SYSTEM\CurrentControlSet\Control\Session Manager\Environment') }
        try {
            $present = $null -ne $key -and @($key.GetValueNames()) -contains 'Path'
            $raw = if ($present) { $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) } else { $null }
            $kind = if ($present) { $key.GetValueKind('Path').ToString() } else { 'absent' }
            $scopes[$scope] = New-QPPathValue -Raw $raw -Present $present -RegistryKind $kind
        } finally { if ($null -ne $key) { $key.Dispose() } }
    }
    return [ordered]@{
        captured_utc = [DateTimeOffset]::UtcNow.ToString('o')
        context = [ordered]@{
            pid = $PID; start_utc = $process.StartTime.ToUniversalTime().ToString('o')
            executable = $process.MainModule.FileName
            executable_sha256 = (Get-FileHash -LiteralPath $process.MainModule.FileName -Algorithm SHA256).Hash.ToLowerInvariant()
            user_sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
            cwd = (Get-Location).Path; shell_version = $PSVersionTable.PSVersion.ToString(); profile_mode = $ProfileMode
            command_line = $cim.CommandLine
            parent = [ordered]@{ pid = $parent.ProcessId; start_utc = $parent.CreationDate.ToUniversalTime().ToString('o'); executable = $parent.ExecutablePath; command_line = $parent.CommandLine }
            launcher = $Launcher; child_overlays = $ChildOverlays
        }
        paths = $scopes
    }
}

function Assert-QPPathPreserved {
    param($Before, $After)
    foreach ($snapshot in @($Before, $After)) {
        Assert-QPFields $snapshot @('captured_utc', 'context', 'paths')
        Assert-QPUtcTimestamp $snapshot.captured_utc
        foreach ($key in @('pid', 'start_utc', 'executable', 'executable_sha256', 'user_sid', 'cwd', 'shell_version', 'profile_mode', 'command_line', 'parent', 'launcher', 'child_overlays')) {
            Assert-QPFields $snapshot.context @($key)
        }
        Assert-QPFields $snapshot.context.parent @('pid', 'start_utc', 'executable', 'command_line')
        Assert-QPTick $snapshot.context.pid
        Assert-QPTick $snapshot.context.parent.pid
        Assert-QPUtcTimestamp $snapshot.context.start_utc
        Assert-QPUtcTimestamp $snapshot.context.parent.start_utc
        if ($snapshot.context.pid -le 0 -or $snapshot.context.parent.pid -le 0 -or
            $snapshot.context.launcher.Count -eq 0 -or $snapshot.context.executable_sha256 -notmatch '^[0-9a-f]{64}$') { throw 'STOP: incomplete process identity' }
        foreach ($key in @('start_utc', 'executable', 'user_sid', 'cwd', 'shell_version', 'profile_mode', 'command_line')) {
            if ([string]::IsNullOrWhiteSpace($snapshot.context[$key])) { throw 'STOP: incomplete process provenance' }
        }
        foreach ($scope in @('process', 'user', 'machine')) {
            $value = $snapshot.paths[$scope]
            if (@($value.Keys) -cnotcontains 'raw') { throw 'STOP: missing raw PATH evidence' }
            Assert-QPFields $value @('present', 'registry_kind')
            if ($value.present -and $null -eq $value.raw) { throw 'STOP: present PATH has no raw value' }
            if ($scope -eq 'process' -and $value.registry_kind -cne 'process') { throw 'STOP: invalid process PATH kind' }
            if ($scope -ne 'process' -and (($value.present -and $value.registry_kind -notin @('String', 'ExpandString')) -or
                (-not $value.present -and $value.registry_kind -cne 'absent'))) { throw 'STOP: unsupported registry PATH kind' }
            Assert-QPEqual (New-QPPathValue -Raw $value.raw -Present $value.present -RegistryKind $value.registry_kind) $value 'PATH fingerprint/segments'
        }
    }
    Assert-QPEqual $Before.context $After.context 'coordinator process/launcher context'
    Assert-QPEqual $Before.paths $After.paths 'raw PATH'
}

function Get-QPChildEnvironment {
    param([System.Collections.IDictionary]$Base, [System.Collections.IDictionary]$Overlay)
    $child = @{}
    foreach ($key in $Base.Keys) { $child[$key] = $Base[$key] }
    foreach ($key in $Overlay.Keys) {
        if ($null -eq $Overlay[$key]) { [void]$child.Remove($key) } else { $child[$key] = $Overlay[$key] }
    }
    return $child # Recipe only: does not modify this process or launch a child.
}

function Get-QPClock {
    return [ordered]@{ utc = [DateTimeOffset]::UtcNow.ToString('o'); ticks = [Diagnostics.Stopwatch]::GetTimestamp(); frequency = [Diagnostics.Stopwatch]::Frequency }
}

function Invoke-QPReadOnlyCli {
    param([string]$Executable, [string]$Arguments, [string]$WorkingDirectory, [int]$TimeoutMs = 15000)
    if (-not [IO.Path]::IsPathRooted($Executable) -or -not [IO.File]::Exists($Executable)) { throw 'STOP: absolute existing Docker CLI path required' }
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Executable; $info.Arguments = $Arguments; $info.WorkingDirectory = $WorkingDirectory
    $info.UseShellExecute = $false; $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $info
    $start = Get-QPClock
    $started = $false
    try {
        [void]$process.Start()
        $started = $true
        $processId = $process.Id
        $processStart = $process.StartTime.ToUniversalTime().ToString('o')
        $readers = @($process.StandardOutput, $process.StandardError)
        $buffers = @((New-Object char[] 4096), (New-Object char[] 4096))
        $builders = @((New-Object Text.StringBuilder), (New-Object Text.StringBuilder))
        $tasks = @($readers[0].ReadAsync($buffers[0], 0, 4096), $readers[1].ReadAsync($buffers[1], 0, 4096))
        $timer = [Diagnostics.Stopwatch]::StartNew()
        while ($null -ne $tasks[0] -or $null -ne $tasks[1]) {
            if ($timer.ElapsedMilliseconds -gt $TimeoutMs) { throw 'STOP: Docker CLI timeout' }
            for ($i = 0; $i -lt 2; $i++) {
                if ($null -eq $tasks[$i] -or -not $tasks[$i].IsCompleted) { continue }
                $count = $tasks[$i].GetAwaiter().GetResult()
                if ($count -eq 0) { $tasks[$i] = $null; continue }
                if ($builders[0].Length + $builders[1].Length + $count -gt 8388608) { throw 'STOP: Docker CLI output exceeded bound' }
                [void]$builders[$i].Append($buffers[$i], 0, $count)
                $tasks[$i] = $readers[$i].ReadAsync($buffers[$i], 0, 4096)
            }
            if ($null -ne $tasks[0] -or $null -ne $tasks[1]) { [Threading.Thread]::Sleep(5) }
        }
        if (-not $process.WaitForExit([Math]::Max(1, $TimeoutMs - [int]$timer.ElapsedMilliseconds))) { throw 'STOP: Docker CLI timeout' }
        if ($process.ExitCode -ne 0) { throw 'STOP: Docker CLI failed' }
        return [ordered]@{ start = $start; end = Get-QPClock; pid = $processId; process_start_utc = $processStart; raw = $builders[0].ToString() }
    } catch {
        if ($started -and -not $process.HasExited) { $process.Kill() }
        throw 'STOP: bounded read-only CLI capture failed (private child output suppressed)'
    } finally { $process.Dispose() }
}

function Read-QPHttpResponse {
    param([byte[]]$Bytes)
    $ascii = [Text.Encoding]::ASCII.GetString($Bytes)
    $separator = $ascii.IndexOf("`r`n`r`n", [StringComparison]::Ordinal)
    if ($separator -lt 0 -or $ascii -notmatch '^HTTP/1\.[01] 200 ') { throw 'STOP: invalid Docker HTTP response' }
    $headers = $ascii.Substring(0, $separator)
    $position = $separator + 4
    $body = New-Object IO.MemoryStream
    try {
        if ($headers -match '(?im)^Transfer-Encoding:\s*chunked\s*$') {
            while ($true) {
                $lineEnd = $ascii.IndexOf("`r`n", $position, [StringComparison]::Ordinal)
                if ($lineEnd -lt 0) { throw 'STOP: truncated HTTP chunk' }
                $sizeText = $ascii.Substring($position, $lineEnd - $position)
                if ($sizeText -notmatch '^[0-9a-fA-F]{1,7}$') { throw 'STOP: invalid HTTP chunk size' }
                $size = [Convert]::ToInt32($sizeText, 16); $position = $lineEnd + 2
                if ($size -gt 16777216 -or $position + $size + 2 -gt $Bytes.Length -or $ascii.Substring($position + $size, 2) -cne "`r`n") { throw 'STOP: truncated/oversize HTTP chunk' }
                if ($size -eq 0) {
                    if ($position + 2 -ne $Bytes.Length) { throw 'STOP: unsupported HTTP trailer/extra bytes' }
                    break
                }
                $body.Write($Bytes, $position, $size); $position += $size + 2
            }
        } else {
            if ($headers -match '(?im)^Transfer-Encoding:') { throw 'STOP: unsupported transfer encoding' }
            if ($headers -match '(?im)^Content-Length:\s*([0-9]+)\s*$') {
                if ([long]$Matches[1] -ne $Bytes.Length - $position) { throw 'STOP: HTTP content length mismatch' }
            }
            $body.Write($Bytes, $position, $Bytes.Length - $position)
        }
        $encoding = New-Object Text.UTF8Encoding($false, $true)
        return [ordered]@{ raw = $encoding.GetString($Bytes); raw_base64 = [Convert]::ToBase64String($Bytes); headers = $headers; body = $encoding.GetString($body.ToArray()) }
    } finally { $body.Dispose() }
}

function Invoke-QPReadOnlyPipe {
    param([string]$PipeName, [ValidateRange(100, 15000)][int]$TimeoutMs = 15000)
    $pipe = New-Object IO.Pipes.NamedPipeClientStream('.', $PipeName, [IO.Pipes.PipeDirection]::InOut, [IO.Pipes.PipeOptions]::Asynchronous)
    $bytes = New-Object IO.MemoryStream
    $clock = [Diagnostics.Stopwatch]::StartNew()
    $start = Get-QPClock
    $pending = $null
    try {
        $pipe.Connect([Math]::Min(3000, $TimeoutMs))
        $request = [Text.Encoding]::ASCII.GetBytes("GET /info HTTP/1.0`r`nHost: localhost`r`nConnection: close`r`n`r`n")
        $pending = $pipe.WriteAsync($request, 0, $request.Length)
        if (-not $pending.Wait([Math]::Min(3000, $TimeoutMs))) { throw 'STOP: pipe write timeout' }
        [void]$pending.GetAwaiter().GetResult()
        $buffer = New-Object byte[] 8192
        while ($true) {
            $remaining = $TimeoutMs - [int]$clock.ElapsedMilliseconds
            if ($remaining -le 0) { throw 'STOP: pipe response timeout' }
            $pending = $pipe.ReadAsync($buffer, 0, $buffer.Length)
            if (-not $pending.Wait($remaining)) { throw 'STOP: pipe response timeout' }
            $count = $pending.GetAwaiter().GetResult()
            if ($count -eq 0) { break }
            $bytes.Write($buffer, 0, $count)
            if ($bytes.Length -gt 16777216) { throw 'STOP: pipe response exceeded bound' }
        }
        $response = Read-QPHttpResponse $bytes.ToArray()
        return [ordered]@{ start = $start; end = Get-QPClock; pid = $PID; process_start_utc = [Diagnostics.Process]::GetCurrentProcess().StartTime.ToUniversalTime().ToString('o'); raw = $response.raw; raw_base64 = $response.raw_base64; body = $response.body }
    } catch { throw 'STOP: bounded direct-pipe capture failed (private response suppressed)' }
    finally {
        # Dispose the pipe first to cancel outstanding I/O. Task owns its wait
        # machinery; never close an OVERLAPPED wait handle while I/O is pending.
        $pipe.Dispose()
        if ($null -ne $pending) { try { [void]$pending.Wait(1000) } catch { } }
        $bytes.Dispose()
    }
}

function Get-QPDockerOverrides {
    $overrides = @{}
    foreach ($name in @('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_API_VERSION', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH')) {
        $value = [Environment]::GetEnvironmentVariable($name, 'Process')
        if (-not [string]::IsNullOrEmpty($value)) { $overrides[$name] = $value }
    }
    return $overrides
}

function Get-QPEngineObservation {
    [CmdletBinding()]
    param([string]$DockerExecutable, [string]$Context, [string]$Endpoint, [string]$WorkingDirectory)
    if ($Context -notmatch '^[a-zA-Z0-9_.-]+$' -or $Endpoint -notmatch '^npipe:////\./pipe/([a-zA-Z0-9_.-]+)$') { throw 'STOP: invalid context/endpoint' }
    $pipeName = $Matches[1]
    if ((Get-QPDockerOverrides).Count -ne 0) { throw 'STOP: Docker environment overrides are set' }
    $contextResult = Invoke-QPReadOnlyCli $DockerExecutable "context inspect $Context" $WorkingDirectory
    $contexts = Read-QPJson $contextResult.raw
    if (@($contexts).Count -ne 1 -or $contexts[0]['Name'] -cne $Context -or $contexts[0]['Endpoints']['docker']['Host'] -cne $Endpoint) { throw 'STOP: Docker context endpoint mismatch' }
    $result = @()
    foreach ($transport in @('pipe', 'cli')) {
        $capture = if ($transport -eq 'pipe') { Invoke-QPReadOnlyPipe $pipeName }
        else { Invoke-QPReadOnlyCli $DockerExecutable "--context $Context info --format `"{{json .}}`"" $WorkingDirectory }
        $body = if ($transport -eq 'pipe') { $capture.body } else { $capture.raw }
        $info = Read-QPJson $body
        $result += [ordered]@{
            request_id = [Guid]::NewGuid().ToString('N'); transport = $transport
            context = $Context; endpoint = $Endpoint; engine_id = $info['ID']; version = $info['ServerVersion']
            system_time = $info['SystemTime']; capture = $capture
            cli_executable = $DockerExecutable; cli_sha256 = (Get-FileHash -LiteralPath $DockerExecutable -Algorithm SHA256).Hash.ToLowerInvariant()
            context_capture = $contextResult
        }
    }
    return ,$result
}

function Assert-QPFreshEngine {
    param([array]$Observations, $Identity, [double]$TimingToleranceSeconds = 2)
    if ([double]::IsNaN($TimingToleranceSeconds) -or $TimingToleranceSeconds -le 0 -or $TimingToleranceSeconds -gt 2) { throw 'STOP: invalid freshness tolerance' }
    $ids = @{}; $previous = @{}; $counts = @{ pipe = 0; cli = 0 }; $cliStarts = @{}
    foreach ($o in $Observations) {
        Assert-QPFields $o @('request_id', 'transport', 'context', 'endpoint', 'engine_id', 'version', 'system_time', 'capture')
        if ([string]::IsNullOrWhiteSpace($o.request_id)) { throw 'STOP: missing request identity' }
        if ($o.transport -notin @('pipe', 'cli') -or $ids.ContainsKey($o.request_id)) { throw 'STOP: reused/unknown observation' }
        $ids[$o.request_id] = $true
        foreach ($key in @('context', 'endpoint', 'engine_id', 'version')) { Assert-QPEqual $Identity[$key] $o[$key] "Engine $key" }
        if ([string]::IsNullOrWhiteSpace($o.capture.raw) -or [string]::IsNullOrWhiteSpace($o.capture.process_start_utc)) { throw 'STOP: missing raw/request provenance' }
        $body = if ($o.transport -eq 'pipe') { $o.capture.body } else { $o.capture.raw }
        if ($o.transport -eq 'pipe') {
            $response = Read-QPHttpResponse ([Convert]::FromBase64String($o.capture.raw_base64))
            Assert-QPEqual $response.raw $o.capture.raw 'raw pipe bytes'
            Assert-QPEqual $response.body $body 'raw pipe body'
        }
        $rawInfo = Read-QPJson $body
        Assert-QPEqual $rawInfo['SystemTime'] $o.system_time 'raw SystemTime'
        Assert-QPEqual $rawInfo['ID'] $o.engine_id 'raw Engine ID'
        Assert-QPEqual $rawInfo['ServerVersion'] $o.version 'raw Engine version'
        $time = [DateTimeOffset]::Parse($o.system_time, [Globalization.CultureInfo]::InvariantCulture)
        $start = $o.capture.start; $end = $o.capture.end
        Assert-QPTick $start.ticks
        Assert-QPTick $end.ticks
        if ($o.capture.pid -le 0) { throw 'STOP: missing capture process identity' }
        if ($start.frequency -le 0 -or $start.frequency -ne $end.frequency -or $end.ticks -lt $start.ticks) { throw 'STOP: invalid monotonic request clock' }
        if ($o.transport -eq 'cli') {
            $instance = "$($o.capture.pid)/$($o.capture.process_start_utc)"
            if ($cliStarts.ContainsKey($instance)) { throw 'STOP: CLI process reused' }
            $cliStarts[$instance] = $true
        }
        if ($previous.ContainsKey($o.transport)) {
            $p = $previous[$o.transport]
            if ($start.frequency -ne $p.capture.start.frequency) { throw 'STOP: monotonic clock source changed' }
            $elapsed = ($start.ticks - $p.capture.start.ticks) / [double]$start.frequency
            $advance = ($time - [DateTimeOffset]::Parse($p.system_time)).TotalSeconds
            $wallAdvance = ([DateTimeOffset]::Parse($start.utc) - [DateTimeOffset]::Parse($p.capture.start.utc)).TotalSeconds
            if ($elapsed -lt 1 -or $advance -le 0 -or [Math]::Abs($advance - $elapsed) -gt $TimingToleranceSeconds -or [Math]::Abs($wallAdvance - $elapsed) -gt $TimingToleranceSeconds) { throw 'STOP: cached or ambiguous Engine freshness' }
        }
        $previous[$o.transport] = $o; $counts[$o.transport]++
    }
    if ($counts.pipe -lt 2 -or $counts.cli -lt 2) { throw 'STOP: independent repeated observations required' }
    $pipe = $previous.pipe; $cli = $previous.cli
    $pipeOffset = ([DateTimeOffset]::Parse($pipe.system_time) - [DateTimeOffset]::Parse($pipe.capture.start.utc)).TotalSeconds
    $cliOffset = ([DateTimeOffset]::Parse($cli.system_time) - [DateTimeOffset]::Parse($cli.capture.start.utc)).TotalSeconds
    if ([Math]::Abs($pipeOffset - $cliOffset) -gt $TimingToleranceSeconds) { throw 'STOP: independent Engine clocks disagree' }
}

function Read-QPAttachmentSource {
    param($Source, [switch]$Inspection)
    Assert-QPFields $Source @('raw', 'raw_sha256', 'ticks')
    Assert-QPTick $Source.ticks
    if ($Source.raw -isnot [string] -or $Source.raw_sha256 -cne (Get-QPSha256 $Source.raw)) { throw 'STOP: attachment source digest mismatch' }
    $value = Read-QPJson $Source.raw
    if ($Inspection -and $value -is [array]) {
        if ($value.Count -ne 1) { throw 'STOP: ambiguous Docker inspection response' }
        $value = $value[0]
    }
    if ($value -isnot [Collections.IDictionary]) { throw 'STOP: Docker source object required' }
    return ,$value
}

function Read-QPAttachmentEvent {
    param($Event, [string]$Type)
    Assert-QPFields $Event @('object_id', 'action', 'docker_source')
    $raw = Read-QPAttachmentSource $Event.docker_source
    Assert-QPFields $raw @('Type', 'Action', 'Actor', 'timeNano')
    Assert-QPFields $raw.Actor @('ID', 'Attributes')
    Assert-QPTick $raw.timeNano
    if ($raw.Type -cne $Type -or $raw.Action -cne $Event.action -or $raw.Actor.ID -cne $Event.object_id -or
        $Event.docker_source.ticks -ne $Event.ticks) { throw 'STOP: Docker event identity was changed' }
    return ,$raw
}

function Assert-QPBridgeAttachments {
    param($Events, $Coverage, $Baseline, $Proof, $Session, $Inventory, $EndTicks)
    try {
        if ($null -eq $Proof -or $null -eq $Session -or $null -eq $Inventory) { throw 'STOP: bridge attachment reconciliation required' }
        Assert-QPFields $Proof @('engine', 'authorization_sha256', 'receipts', 'transitions')
        Assert-QPEqual $Session.identity $Proof.engine 'attachment Engine identity'
        if ($Proof.authorization_sha256 -cne $Session.authorization_sha256 -or $Proof.receipts -isnot [array] -or
            $Proof.transitions -isnot [array] -or $Proof.transitions.Count -ne $Events.Count) { throw 'STOP: attachment authorization/transition mismatch' }
        $bridges = @($Baseline.networks | Where-Object { $_['Name'] -ceq 'bridge' })
        if ($bridges.Count -ne 1) { throw 'STOP: ambiguous baseline bridge' }
        $bridge = $bridges[0]
        Assert-QPFields $bridge @('Id', 'Created', 'Containers')
        if ($bridge.Containers -isnot [Collections.IDictionary]) { throw 'STOP: baseline endpoint map required' }
        # Quiescent full inventory equality remains mandatory; no field is waived.
        Assert-QPEqual $Baseline.networks $Inventory.networks 'final protected networks/endpoints'
        $owners = @{}; $active = @{}; $seen = @{}; $lastTicks = -1; $lastNano = -1
        for ($index = 0; $index -lt $Events.Count; $index++) {
            $event = $Events[$index]
            $raw = Read-QPAttachmentEvent $event 'network'
            Assert-QPFields $raw.Actor.Attributes @('container', 'name')
            $cid = $raw.Actor.Attributes.container
            if ($raw.Actor.ID -cne $bridge.Id -or $raw.Actor.Attributes.name -cne 'bridge' -or
                $raw.Action -cnotin @('connect', 'disconnect') -or $cid -isnot [string] -or $cid -cnotmatch '^[0-9a-f]{64}$' -or
                $event.ticks -le $lastTicks -or $raw.timeNano -le $lastNano -or $seen.ContainsKey($event.docker_source.raw_sha256)) { throw 'STOP: ambiguous bridge event/lifecycle' }
            $lastTicks = $event.ticks; $lastNano = $raw.timeNano; $seen[$event.docker_source.raw_sha256] = $true
            if (-not $owners.ContainsKey($cid)) {
                if (@($Baseline.containers | Where-Object { $_['Id'] -ceq $cid -or $_['ID'] -ceq $cid }).Count -ne 0 -or @($bridge.Containers.Keys) -ccontains $cid) { throw 'STOP: protected container attachment' }
                $receipts = @($Proof.receipts | Where-Object { $_['container_id'] -ceq $cid })
                if ($receipts.Count -ne 1) { throw 'STOP: missing/ambiguous creation receipt' }
                $receipt = $receipts[0]
                Assert-QPFields $receipt @('plan_id', 'create_event_sha256', 'inspection')
                $plans = @($Session.test_container_plans | Where-Object { $_['plan_id'] -ceq $receipt.plan_id })
                if ($plans.Count -ne 1) { throw 'STOP: container not in original approved plan' }
                $plan = $plans[0]
                $creates = @(@($Coverage | Where-Object { $_.kind -ceq 'container' })[0].events | Where-Object {
                    $_.object_id -ceq $cid -and $_.action -ceq 'create'
                })
                if ($creates.Count -ne 1) { throw 'STOP: missing/ambiguous observed creation event' }
                $create = $creates[0]; $created = Read-QPAttachmentEvent $create 'container'
                $actual = Read-QPAttachmentSource $receipt.inspection -Inspection
                Assert-QPFields $actual @('Id', 'Created', 'Name', 'Image', 'Config', 'Mounts')
                Assert-QPFields $actual.Config @('Cmd', 'Image')
                Assert-QPFields $created.Actor.Attributes @('name', 'image')
                $creationTime = [DateTimeOffset]::Parse($actual.Created, [Globalization.CultureInfo]::InvariantCulture)
                $creationNano = [decimal]($creationTime.UtcTicks - [DateTimeOffset]::Parse('1970-01-01T00:00:00Z').UtcTicks) * 100
                if ($create.docker_source.raw_sha256 -cne $receipt.create_event_sha256 -or $created.timeNano -ge $raw.timeNano -or
                    $create.ticks -lt $Session.coverage_start_ticks -or $create.ticks -gt $EndTicks -or
                    $receipt.inspection.ticks -lt $create.ticks -or $receipt.inspection.ticks -gt $EndTicks -or
                    $actual.Id -cne $cid -or $actual.Created -isnot [string] -or [string]::IsNullOrWhiteSpace($actual.Created) -or
                    $actual.Name -cne ('/' + $created.Actor.Attributes.name) -or $actual.Config.Image -cne $created.Actor.Attributes.image -or
                    $actual.Image -cne $plan.image_id -or $creationNano -lt 0 -or $creationNano -gt $created.timeNano) { throw 'STOP: creation receipt/event identity mismatch' }
                Assert-QPEqual $plan.command $actual.Config.Cmd 'approved container command'
                $binds = @($actual.Mounts | Where-Object { $_['Type'] -ceq 'bind' -and $_['Source'] -ceq $plan.project_path -and $_['Destination'] -ceq '/workspace' })
                if ($binds.Count -ne 1) { throw 'STOP: missing exact test-owned project bind' }
                $owners[$cid] = $actual.Name.Substring(1)
            }
            $transitions = @($Proof.transitions | Where-Object { $_['event_sha256'] -ceq $event.docker_source.raw_sha256 })
            if ($transitions.Count -ne 1) { throw 'STOP: missing/ambiguous endpoint observation' }
            $source = $transitions[0].network_inspection
            $network = Read-QPAttachmentSource $source -Inspection
            if ($source.ticks -lt $event.ticks -or $source.ticks -gt $EndTicks -or
                ($index + 1 -lt $Events.Count -and $source.ticks -ge $Events[$index + 1].ticks)) { throw 'STOP: endpoint observation outside event window' }
            Assert-QPFields $network @('Id', 'Created', 'Containers')
            if ($network.Containers -isnot [Collections.IDictionary]) { throw 'STOP: actual endpoint map required' }
            $withoutEndpoints = Read-QPJson (ConvertTo-QPCanonicalJson $network)
            $withoutEndpoints['Containers'] = $bridge.Containers
            Assert-QPEqual $bridge $withoutEndpoints 'bridge identity/configuration during attachment'
            if ($raw.Action -ceq 'connect') {
                if ($active.ContainsKey($cid) -or @($network.Containers.Keys) -cnotcontains $cid) { throw 'STOP: unmatched/duplicate bridge connection' }
                $endpoint = $network.Containers[$cid]
                Assert-QPFields $endpoint @('Name', 'EndpointID')
                if ($endpoint.Name -cne $owners[$cid] -or $endpoint.EndpointID -cnotmatch '^[0-9a-f]{64}$') { throw 'STOP: endpoint identity mismatch' }
                foreach ($key in $network.Containers.Keys) {
                    if ($key -cne $cid -and $network.Containers[$key].EndpointID -ceq $endpoint.EndpointID) { throw 'STOP: ambiguous endpoint identity' }
                }
                $active[$cid] = $endpoint
            } else {
                if (-not $active.ContainsKey($cid)) { throw 'STOP: unmatched bridge disconnect' }
                $active.Remove($cid)
            }
            $expected = Read-QPJson (ConvertTo-QPCanonicalJson $bridge.Containers)
            foreach ($key in $active.Keys) { $expected[$key] = $active[$key] }
            Assert-QPEqual $expected $network.Containers 'actual network endpoints'
        }
        if ($active.Count -ne 0 -or $owners.Count -ne $Proof.receipts.Count) { throw 'STOP: unreconciled/ambiguous test attachment lifecycle' }
    } catch { throw 'STOP: invalid protected bridge attachment evidence/reconciliation' }
}

function Assert-QPObservationCoverage {
    param($Coverage, $StartTicks, $EndTicks, $ProtectedInventory, $BridgeAttachments = $null, $Session = $null, $Inventory = $null)
    Assert-QPTick $StartTicks
    Assert-QPTick $EndTicks
    if ($EndTicks -lt $StartTicks) { throw 'STOP: invalid coverage window' }
    $bridgeEvents = New-Object 'Collections.Generic.List[object]'
    foreach ($kind in @('container', 'network', 'lifecycle')) {
        $streams = @($Coverage | Where-Object { $_.kind -ceq $kind })
        if ($streams.Count -ne 1) { throw 'STOP: missing or ambiguous observation stream' }
        $s = $streams[0]
        Assert-QPFields $s @('start_ticks', 'end_ticks')
        Assert-QPTick $s.start_ticks
        Assert-QPTick $s.end_ticks
        Assert-QPFields $s @('raw', 'raw_sha256', 'provenance', 'events', 'gaps')
        Assert-QPFields $s.provenance @('pid', 'process_start_utc', 'executable_sha256', 'source')
        Assert-QPTick $s.provenance.pid
        Assert-QPUtcTimestamp $s.provenance.process_start_utc
        if ($s.provenance.pid -le 0 -or $s.provenance.executable_sha256 -isnot [string] -or
            $s.provenance.executable_sha256 -cnotmatch '^[0-9a-f]{64}$' -or $s.provenance.source -isnot [string] -or
            [string]::IsNullOrWhiteSpace($s.provenance.source)) { throw 'STOP: incomplete observer provenance' }
        if ($s.raw_sha256 -cne (Get-QPSha256 $s.raw)) { throw 'STOP: observer evidence digest mismatch' }
        $record = Read-QPJson $s.raw
        foreach ($field in @('kind', 'observer_id', 'complete', 'exit_reason', 'start_ticks', 'end_ticks', 'gaps', 'events', 'provenance')) {
            Assert-QPEqual $record[$field] $s[$field] 'raw observer record'
        }
        if ($s.complete -isnot [bool] -or -not $s.complete -or $s.exit_reason -cne 'coordinator-finalized' -or @($s.gaps).Count -ne 0 -or
            $s.start_ticks -gt $StartTicks -or $s.end_ticks -lt $EndTicks -or [string]::IsNullOrWhiteSpace($s.observer_id) -or
            [string]::IsNullOrWhiteSpace($s.raw_sha256) -or $s.raw_sha256 -notmatch '^[0-9a-f]{64}$') { throw 'STOP: interrupted/incomplete observation coverage' }
        :events foreach ($event in $s.events) {
            Assert-QPFields $event @('ticks')
            Assert-QPTick $event.ticks
            # Validate supplied Docker identities before wrapper-based classification.
            # A substituted object_id must not route a protected event to the generic branch.
            if ($kind -cin @('container', 'network') -and @($event.Keys) -ccontains 'docker_source') {
                $null = Read-QPAttachmentEvent $event $kind
            }
            if ($event.ticks -lt $StartTicks -or $event.ticks -gt $EndTicks) { continue }
            if ($kind -eq 'lifecycle') { throw 'STOP: Engine lifecycle/cache transition' }
            if ($null -eq $ProtectedInventory) { throw 'STOP: event has no protected inventory reconciliation' }
            foreach ($area in @('images', 'networks', 'containers', 'volumes')) {
                foreach ($object in $ProtectedInventory[$area]) {
                    foreach ($key in @('Id', 'ID', 'Name')) {
                        if (@($object.Keys) -ccontains $key -and $object[$key] -ceq $event.object_id) {
                            if ($kind -ceq 'network' -and $area -ceq 'networks' -and $key -ceq 'Id' -and $object['Name'] -ceq 'bridge') {
                                $bridgeEvents.Add($event)
                                continue events
                            }
                            throw 'STOP: event changed a protected object'
                        }
                    }
                }
            }
            # Only explicitly receipt-authorized test-object events may occur.
            if ($event.receipt_verified -isnot [bool] -or -not $event.receipt_verified -or [string]::IsNullOrWhiteSpace($event.object_id)) { throw 'STOP: unexpected/unattributed resource event' }
        }
    }
    if ($bridgeEvents.Count -ne 0 -or $null -ne $BridgeAttachments) {
        Assert-QPBridgeAttachments $bridgeEvents.ToArray() $Coverage $ProtectedInventory $BridgeAttachments $Session $Inventory $EndTicks
    }
}

function Assert-QPTick {
    param($Value)
    if (($Value -isnot [int] -and $Value -isnot [long] -and $Value -isnot [decimal]) -or
        $Value -lt 0 -or $Value -gt [long]::MaxValue -or [decimal]::Truncate([decimal]$Value) -ne $Value) { throw 'STOP: missing/ambiguous coordinator tick' }
}

function New-QPPreservationSession {
    param($ColdInventory, $Identity, [string]$AuthorizationSha256, $ObservationStartTicks = -1, $TestContainerPlans = @())
    if ($AuthorizationSha256 -notmatch '^[0-9a-f]{64}$') { throw 'STOP: authorization digest required' }
    Assert-QPTick $ObservationStartTicks
    $planIds = @{}
    foreach ($plan in $TestContainerPlans) {
        Assert-QPFields $plan @('plan_id', 'project_path', 'image_id', 'command')
        if ([string]::IsNullOrWhiteSpace($plan.plan_id) -or $planIds.ContainsKey($plan.plan_id) -or
            $plan.project_path -cnotmatch '^[A-Za-z]:\\' -or $plan.image_id -cnotmatch '^sha256:[0-9a-f]{64}$' -or
            $plan.command -isnot [array] -or $plan.command.Count -eq 0 -or @($plan.command | Where-Object { $_ -isnot [string] }).Count -ne 0) { throw 'STOP: invalid approved test-container plan' }
        $planIds[$plan.plan_id] = $true
    }
    return [ordered]@{
        schema_version = 1; phase = 'cold'; baseline_count = 0
        cold = Read-QPJson (ConvertTo-QPCanonicalJson $ColdInventory)
        identity = $Identity; authorization_sha256 = $AuthorizationSha256
        baseline = $null; baseline_ticks = 0; last_ticks = 0; stop_reason = $null; used_requests = @{}
        coverage_start_ticks = $ObservationStartTicks
        test_container_plans = Read-QPJson (ConvertTo-QPCanonicalJson @($TestContainerPlans))
    }
}

function Assert-QPInventory {
    param($Inventory, $Identity)
    Assert-QPFields $Inventory @('engine', 'images', 'containers', 'volumes', 'networks', 'protected_host', 'capture_window')
    Assert-QPFields $Inventory['capture_window'] @('start_ticks', 'end_ticks', 'frequency')
    Assert-QPTick $Inventory['capture_window']['start_ticks']
    Assert-QPTick $Inventory['capture_window']['end_ticks']
    if ($Inventory['capture_window']['end_ticks'] -lt $Inventory['capture_window']['start_ticks']) { throw 'STOP: invalid inventory capture window' }
    Assert-QPEqual $Identity $Inventory['engine'] 'inventory Engine identity'
    Assert-QPFields $Inventory['protected_host'] @('path')
    Assert-QPPathPreserved $Inventory['protected_host']['path'] $Inventory['protected_host']['path']
    foreach ($area in @('images', 'containers', 'volumes', 'networks')) {
        if ($Inventory[$area] -isnot [array]) { throw 'STOP: inventory array required' }
    }
}

function Assert-QPNewObservations {
    param($Session, [array]$Observations, $Ticks, $InventoryWindow)
    Assert-QPTick $Ticks
    Assert-QPFreshEngine $Observations $Session.identity
    $last = (Get-QPObservationBounds $Observations).last_end
    if ($last.ticks -gt $Ticks -or ($Ticks - $last.ticks) / [double]$last.frequency -gt 2) { throw 'STOP: inventory freshness gate expired or clock inconsistent' }
    foreach ($o in $Observations) {
        if ($Session.used_requests.ContainsKey($o.request_id) -or ($Session.baseline_count -gt 0 -and $o.capture.start.ticks -lt $Session.last_ticks)) { throw 'STOP: historical observation reused' }
        $Session.used_requests[$o.request_id] = $true
    }
    foreach ($transport in @('pipe', 'cli')) {
        $samples = @($Observations | Where-Object { $_.transport -ceq $transport })
        if ($InventoryWindow.frequency -ne $samples[0].capture.end.frequency -or
            $samples[0].capture.end.ticks -gt $InventoryWindow.start_ticks -or
            $samples[-1].capture.start.ticks -lt $InventoryWindow.end_ticks) { throw 'STOP: inventory not bracketed by independent freshness observations' }
    }
}

function Get-QPObservationBounds {
    param([array]$Observations)
    # Preserve exact integer ticks; Measure-Object can render numeric extrema as
    # doubles, and array position is not chronological across transports.
    $first = $Observations[0].capture.start.ticks
    $last = $Observations[0].capture.end
    foreach ($o in $Observations) {
        if ($o.capture.start.ticks -lt $first) { $first = $o.capture.start.ticks }
        if ($o.capture.end.ticks -gt $last.ticks) { $last = $o.capture.end }
    }
    return @{ first_start = $first; last_end = $last }
}

function Set-QPWarmBaseline {
    param($Session, $WarmInventory, [array]$Observations, $Ticks, $WakeReceipt = $null)
    try {
        Assert-QPTick $Ticks
        if ($Session.phase -cne 'cold' -or $Session.baseline_count -ne 0) { throw 'STOP: baseline already captured or session stopped' }
        Assert-QPInventory $Session.cold $Session.identity
        Assert-QPInventory $WarmInventory $Session.identity
        Assert-QPNewObservations $Session $Observations $Ticks $WarmInventory['capture_window']
        if ($Session.coverage_start_ticks -gt (Get-QPObservationBounds $Observations).first_start -or
            $Session.coverage_start_ticks -gt $WarmInventory['capture_window']['start_ticks']) { throw 'STOP: observation starts after baseline readiness/capture began' }
        foreach ($area in @('engine', 'images', 'containers', 'volumes', 'protected_host')) { Assert-QPEqual $Session.cold[$area] $WarmInventory[$area] "prebaseline $area" }
        $coldNetworks = $Session.cold['networks']; $warmNetworks = $WarmInventory['networks']
        if ((ConvertTo-QPCanonicalJson $coldNetworks) -cne (ConvertTo-QPCanonicalJson $warmNetworks)) {
            if ($null -eq $WakeReceipt -or $WakeReceipt.authorization_sha256 -cne $Session.authorization_sha256 -or $WakeReceipt.live_verified -isnot [bool] -or -not $WakeReceipt.live_verified) { throw 'STOP: unapproved prebaseline network transition' }
            Assert-QPEqual $coldNetworks $WakeReceipt.before_networks 'wake receipt prior networks'
            Assert-QPEqual $warmNetworks $WakeReceipt.after_networks 'wake receipt current networks'
            # Exact receipt-bound bridge identity replacement only; all other
            # fields and all other networks must remain identical.
            $reconciled = Read-QPJson (ConvertTo-QPCanonicalJson $warmNetworks)
            $oldBridge = @($coldNetworks | Where-Object { $_['Name'] -ceq 'bridge' })
            $newBridge = @($reconciled | Where-Object { $_['Name'] -ceq 'bridge' })
            if ($oldBridge.Count -ne 1 -or $newBridge.Count -ne 1) { throw 'STOP: ambiguous bridge transition' }
            $newBridge[0]['Id'] = $oldBridge[0]['Id']; $newBridge[0]['Created'] = $oldBridge[0]['Created']
            Assert-QPEqual $coldNetworks $reconciled 'prebaseline protected network configuration'
        }
        $Session.baseline = Read-QPJson (ConvertTo-QPCanonicalJson $WarmInventory)
        $Session.baseline_ticks = $Ticks; $Session.last_ticks = $Ticks
        $Session.baseline_count = 1; $Session.phase = 'observing'
    } catch {
        if ($Session.phase -notin @('stopped', 'complete')) { $Session.phase = 'stopped'; $Session.stop_reason = $_.Exception.Message }
        throw
    }
}

function Assert-QPPreservation {
    param($Session, $Inventory, [array]$Observations, $Coverage, $Ticks, [switch]$Final, $BridgeAttachments = $null)
    try {
        Assert-QPTick $Ticks
        if ($Session.phase -cne 'observing' -or $Ticks -lt $Session.last_ticks) { throw 'STOP: inactive session or invalid clock' }
        Assert-QPInventory $Inventory $Session.identity
        Assert-QPNewObservations $Session $Observations $Ticks $Inventory['capture_window']
        Assert-QPObservationCoverage $Coverage $Session.coverage_start_ticks $Ticks $Session.baseline $BridgeAttachments $Session $Inventory
        foreach ($area in @('engine', 'images', 'containers', 'volumes', 'networks', 'protected_host')) { Assert-QPEqual $Session.baseline[$area] $Inventory[$area] "protected $area" }
        $Session.last_ticks = $Ticks
        if ($Final) { $Session.phase = 'complete' }
    } catch {
        if ($Session.phase -notin @('stopped', 'complete')) { $Session.phase = 'stopped'; $Session.stop_reason = $_.Exception.Message }
        throw
    }
}

function Open-QPDirectoryPins {
    param([string]$Path)
    if ($null -eq ('QualificationPreservation.DirectoryPin' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
namespace QualificationPreservation {
    public static class DirectoryPin {
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        public static extern SafeFileHandle CreateFileW(string name, uint access,
            uint share, IntPtr security, uint disposition, uint flags, IntPtr template);
    }
}
'@
    }
    $full = [IO.Path]::GetFullPath($Path)
    if ($full -notmatch '^[a-zA-Z]:\\') { throw 'STOP: local drive-letter evidence directory required' }
    $pins = New-Object 'System.Collections.Generic.List[Microsoft.Win32.SafeHandles.SafeFileHandle]'
    $root = [IO.Path]::GetPathRoot($full)
    $cursor = $root
    $parts = @('') + @($full.Substring($root.Length).Split([char]'\', [StringSplitOptions]::RemoveEmptyEntries))
    try {
        foreach ($part in $parts) {
            if ($part -cne '') { $cursor = Join-Path $cursor $part }
            # GENERIC_READ includes directory-list access: an attributes-only
            # handle does not reliably participate in Windows share checks.
            # Share read/write, deliberately NOT delete. Pin
            # root through leaf before validation, holding every handle through
            # the write. OPEN_REPARSE_POINT avoids following a replaced leaf.
            $handle = [QualificationPreservation.DirectoryPin]::CreateFileW($cursor, [uint32]2147483648, 3, [IntPtr]::Zero, 3, 0x02200000, [IntPtr]::Zero)
            if ($handle.IsInvalid) { $handle.Dispose(); throw 'STOP: cannot pin evidence ancestor' }
            $pins.Add($handle)
            $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop
            if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
                (Test-Path -LiteralPath (Join-Path $cursor '.git'))) { throw 'STOP: repository/reparse evidence destination' }
        }
        return ,$pins.ToArray()
    } catch {
        foreach ($handle in $pins) { $handle.Dispose() }
        throw 'STOP: evidence directory could not be safely pinned'
    }
}

function Write-QPPrivateEvidence {
    [CmdletBinding()]
    param([string]$ExistingPrivateDirectory, [string]$Name, $Evidence, [string]$PreviousSha256)
    # The owner provisions access control. This helper never changes ACLs and
    # never writes inside the repository. Names are append-only, never replaced.
    if (-not [IO.Path]::IsPathRooted($ExistingPrivateDirectory) -or -not [IO.Directory]::Exists($ExistingPrivateDirectory) -or
        $Name -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_.-]*\.json$' -or $PreviousSha256 -notmatch '^[0-9a-f]{64}$') { throw 'STOP: invalid private evidence destination/chain' }
    $pins = Open-QPDirectoryPins $ExistingPrivateDirectory
    try {
        $allowedSids = @([Security.Principal.WindowsIdentity]::GetCurrent().User.Value, 'S-1-5-18')
        $acl = Get-Acl -LiteralPath $ExistingPrivateDirectory
        $descriptor = New-Object Security.AccessControl.RawSecurityDescriptor($acl.GetSecurityDescriptorBinaryForm(), 0)
        if ($null -eq $descriptor.DiscretionaryAcl -or $allowedSids -notcontains $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value) { throw 'STOP: private evidence owner/DACL is unsafe' }
        foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
            if ($rule.AccessControlType -eq 'Allow' -and $allowedSids -notcontains $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value) { throw 'STOP: private evidence directory grants access to another identity' }
        }
        $payload = ConvertTo-QPCanonicalJson $Evidence
        $record = [ordered]@{ schema_version = 1; recorded_utc = [DateTimeOffset]::UtcNow.ToString('o'); previous_sha256 = $PreviousSha256; payload_sha256 = Get-QPSha256 $payload; payload = $Evidence }
        $text = ConvertTo-QPCanonicalJson $record
        $path = Join-Path $ExistingPrivateDirectory $Name
        $stream = New-Object IO.FileStream($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $encoding = New-Object Text.UTF8Encoding($false, $true); $bytes = $encoding.GetBytes($text); $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true) }
        finally { $stream.Dispose() }
        return [ordered]@{ name = $Name; sha256 = Get-QPSha256 $text }
    } finally { foreach ($handle in $pins) { $handle.Dispose() } }
}

function Assert-QPPrivateEvidenceChain {
    param([string]$Directory, [array]$Records, [string]$FinalSha256)
    $previous = '0' * 64
    foreach ($entry in $Records) {
        if ($entry.name -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_.-]*\.json$') { throw 'STOP: invalid chain filename' }
        $raw = [IO.File]::ReadAllText((Join-Path $Directory $entry.name), (New-Object Text.UTF8Encoding($false, $true)))
        $hash = Get-QPSha256 $raw
        if ($hash -cne $entry.sha256) { throw 'STOP: sealed evidence hash mismatch' }
        $record = Read-QPJson $raw
        if ($record['previous_sha256'] -cne $previous -or $record['payload_sha256'] -cne (Get-QPSha256 (ConvertTo-QPCanonicalJson $record['payload']))) { throw 'STOP: evidence chain/payload mismatch' }
        $previous = $hash
    }
    if ($Records.Count -eq 0 -or $previous -cne $FinalSha256) { throw 'STOP: evidence chain anchor mismatch' }
}
