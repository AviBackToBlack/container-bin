#Requires -Version 5.1
[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'qualification-preservation.ps1')
$fixture = Read-QPJson ([IO.File]::ReadAllText((Join-Path $PSScriptRoot 'testdata\qualification-preservation\synthetic.json')))
$script:passed = 0
$script:observationBatch = 0
function Test-QPCase {
    param([string]$Name, [scriptblock]$Action)
    & $Action
    $script:passed++
    Write-Output "PASS: $Name"
}
function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}
function Assert-Stop {
    param([scriptblock]$Action)
    $stopped = $false
    try { & $Action } catch { $stopped = $_.Exception.Message -match 'STOP:' }
    Assert-True $stopped 'Expected a fail-closed STOP'
}
function Copy-Fixture { param($Value) return ,(Read-QPJson (ConvertTo-QPCanonicalJson $Value)) }
function New-PathFixture {
    $scopes = [ordered]@{}
    foreach ($scope in @('process', 'user', 'machine')) {
        $kind = if ($scope -eq 'process') { 'process' } else { 'String' }
        $scopes[$scope] = New-QPPathValue $fixture['path'] $true $kind
    }
    return [ordered]@{
        captured_utc = '2030-01-01T00:00:01Z'
        context = [ordered]@{
            pid = 100; start_utc = '2030-01-01T00:00:00Z'; executable = 'C:\Synthetic\shell.exe'
            executable_sha256 = 'a' * 64; user_sid = 'synthetic-user'; cwd = 'C:\Synthetic\project'
            shell_version = '5.1'; profile_mode = 'NoProfile'; command_line = 'synthetic invocation'
            parent = @{ pid = 99; start_utc = '2030-01-01T00:00:00Z'; executable = 'synthetic launcher'; command_line = 'synthetic' }
            launcher = @{ recipe = 'synthetic'; run_id = 'fixture' }; child_overlays = @{ smoke = @{ PATH = 'C:\Synthetic\Private;' + $fixture['path'] } }
        }
        paths = $scopes
    }
}
function New-Observations {
    param([int]$OffsetSeconds = -3600, [long]$StartTicks = 1000)
    $script:observationBatch++
    $observations = @()
    for ($i = 0; $i -lt 4; $i++) {
        $hostTime = [DateTimeOffset]::Parse('2030-01-01T12:00:00Z').AddSeconds($i * 3)
        $body = [ordered]@{ ID = $fixture['identity']['engine_id']; ServerVersion = '29.0.0'; SystemTime = $hostTime.AddSeconds($OffsetSeconds).ToString('o') }
        $bodyText = ConvertTo-QPCanonicalJson $body
        $raw = if ($i % 2 -eq 0) { "HTTP/1.0 200 OK`r`n`r`n$bodyText" } else { $bodyText }
        $observations += [ordered]@{
            request_id = "request-$script:observationBatch-$i"; transport = @('pipe', 'cli')[$i % 2]
            context = $fixture['identity']['context']; endpoint = $fixture['identity']['endpoint']; engine_id = $body.ID; version = $body.ServerVersion
            system_time = $body.SystemTime
            capture = [ordered]@{
                pid = 200 + $i; process_start_utc = $hostTime.ToString('o')
                start = @{ utc = $hostTime.ToString('o'); ticks = $StartTicks + ($i * 3000); frequency = 1000 }
                end = @{ utc = $hostTime.AddMilliseconds(20).ToString('o'); ticks = $StartTicks + 20 + ($i * 3000); frequency = 1000 }
                raw = $raw; raw_base64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($raw)); body = $bodyText
            }
        }
    }
    return ,$observations
}
function New-Inventory {
    param([long]$StartTicks = 1000)
    return [ordered]@{
        engine = $fixture['identity']; images = @(@{ Id = 'synthetic-image'; RootFS = @{ Layers = @('synthetic-layer') }; Labels = @{ created = $fixture['timestamp_label'] } })
        networks = Copy-Fixture $fixture['networks']; containers = @(); volumes = @()
        protected_host = @{ path = New-PathFixture; settings_sha256 = 'b' * 64 }
        capture_window = @{ start_ticks = $StartTicks + 5000; end_ticks = $StartTicks + 5500; frequency = 1000 }
    }
}
function New-Coverage {
    $coverage = @()
    foreach ($kind in @('container', 'network', 'lifecycle')) {
        $stream = [ordered]@{
            kind = $kind; observer_id = "synthetic-$kind"
            complete = $true; exit_reason = 'coordinator-finalized'; gaps = @(); events = @(); start_ticks = 900; end_ticks = 30000
            provenance = @{ pid = 201; process_start_utc = '2030-01-01T00:00:00Z'; executable_sha256 = 'd' * 64; source = 'synthetic offline capture' }
        }
        $raw = ConvertTo-QPCanonicalJson $stream
        $stream['raw'] = $raw; $stream['raw_sha256'] = Get-QPSha256 $raw
        $coverage += $stream
    }
    return ,$coverage
}
function Seal-Coverage {
    param($Coverage)
    foreach ($stream in $Coverage) {
        $record = [ordered]@{}
        foreach ($key in $stream.Keys) { if ($key -notin @('raw', 'raw_sha256')) { $record[$key] = $stream[$key] } }
        $stream.raw = ConvertTo-QPCanonicalJson $record; $stream.raw_sha256 = Get-QPSha256 $stream.raw
    }
}
function New-ReadySession {
    $inventory = New-Inventory
    $session = New-QPPreservationSession $inventory $fixture['identity'] ('c' * 64) 900
    Set-QPWarmBaseline $session $inventory (New-Observations) 11000
    return $session
}

function New-AttachmentSource {
    param($Value, [long]$Ticks)
    $raw = ConvertTo-QPCanonicalJson $Value
    return @{ raw = $raw; raw_sha256 = Get-QPSha256 $raw; ticks = $Ticks }
}
function New-AttachmentEvent {
    param([string]$Type, [string]$Action, [string]$Id, $Attributes, [long]$Ticks, [long]$Nano)
    return @{ ticks = $Ticks; object_id = $Id; action = $Action; receipt_verified = $true
        docker_source = New-AttachmentSource @{ Type = $Type; Action = $Action; Actor = @{ ID = $Id; Attributes = $Attributes }; timeNano = $Nano } $Ticks }
}
function New-BridgeFixture {
    $baseline = New-Inventory; $cid = 'e' * 64; $image = 'sha256:' + ('a' * 64)
    $bridge = $baseline.networks[0]
    $bridge['Containers']['b' * 64] = @{ Name = 'protected-original'; EndpointID = 'c' * 64; IPv4Address = '10.0.0.2/24' }
    $plan = @{ plan_id = 'approved-private-project'; project_path = 'C:\Synthetic\Owned\project'; image_id = $image; command = @('python', '-c', 'pass') }
    $session = New-QPPreservationSession $baseline $fixture['identity'] ('c' * 64) 900 @($plan)
    Set-QPWarmBaseline $session $baseline (New-Observations) 11000
    $create = New-AttachmentEvent 'container' 'create' $cid @{ name = 'qp-test'; image = $image } 11100 1893495610100000000
    $connect = New-AttachmentEvent 'network' 'connect' $bridge.Id @{ name = 'bridge'; container = $cid } 11200 1893495610200000000
    $disconnect = New-AttachmentEvent 'network' 'disconnect' $bridge.Id @{ name = 'bridge'; container = $cid } 11300 1893495610300000000
    $inspection = @{ Id = $cid; Name = '/qp-test'; Created = '2030-01-01T11:00:10.100000000Z'; Image = $image
        Config = @{ Image = $image; Cmd = @('python', '-c', 'pass') }
        Mounts = @(@{ Type = 'bind'; Source = $plan.project_path; Destination = '/workspace'; RW = $true }) }
    $connected = Copy-Fixture $bridge
    $connected['Containers'][$cid] = @{ Name = 'qp-test'; EndpointID = 'f' * 64; IPv4Address = '10.0.0.3/24' }
    $proof = @{ engine = $fixture['identity']; authorization_sha256 = 'c' * 64
        receipts = @(@{ container_id = $cid; plan_id = $plan.plan_id; create_event_sha256 = $create.docker_source.raw_sha256; inspection = New-AttachmentSource $inspection 11150 })
        transitions = @(
            @{ event_sha256 = $connect.docker_source.raw_sha256; network_inspection = New-AttachmentSource $connected 11250 },
            @{ event_sha256 = $disconnect.docker_source.raw_sha256; network_inspection = New-AttachmentSource $bridge 11350 }
        ) }
    $coverage = New-Coverage; $coverage[0].events = @($create); $coverage[1].events = @($connect, $disconnect)
    Seal-Coverage $coverage
    $current = Copy-Fixture $baseline; $current['capture_window'] = @{ start_ticks = 16000; end_ticks = 16500; frequency = 1000 }
    return @{ session = $session; coverage = $coverage; proof = $proof; current = $current; cid = $cid }
}
function Assert-BridgeFixture {
    param($Case)
    Seal-Coverage $Case.coverage
    Assert-QPPreservation $Case.session $Case.current (New-Observations -StartTicks 11000) $Case.coverage 21000 -Final -BridgeAttachments $Case.proof
}

Test-QPCase 'raw timestamp labels and empty/null arrays survive parsing' {
    Assert-True ($fixture['timestamp_label'] -is [string]) 'Timestamp label converted'
    Assert-True ((ConvertTo-QPCanonicalJson $fixture['timestamp_label']) -ceq '"2030-01-01T02:24:57+00:00"') 'Timestamp string changed'
    Assert-True ((ConvertTo-QPCanonicalJson $fixture['empty_array']) -ceq '[]') 'Empty array changed'
    Assert-True ((ConvertTo-QPCanonicalJson $fixture['null_array']) -ceq '[null]') 'Null array changed'
    Assert-True ((ConvertTo-QPCanonicalJson $fixture['case_sensitive_keys']) -ceq '{"A":"upper","a":"lower"}') 'Case-sensitive keys lost'
    Assert-Stop { ConvertTo-QPCanonicalJson ([datetime]::UtcNow) }
    Assert-Stop { Read-QPJson '{"ID":"first","\u0049D":"second"}' }
    Assert-True ((ConvertTo-QPCanonicalJson (Read-QPJson '[[],[null],[["x"]]]')) -ceq '[[],[null],[["x"]]]') 'Nested array shape changed'
    Assert-True ((Get-QPSha256 'abc') -ceq 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad') 'SHA-256 encoding differs'
}
Test-QPCase 'live independent responses pass with a constant one-hour clock offset' { Assert-QPFreshEngine (New-Observations) $fixture['identity'] }
Test-QPCase 'freshness tolerance rejects NaN, infinity and out-of-range values' {
    foreach ($tolerance in @([double]::NaN, [double]::PositiveInfinity, [double]::NegativeInfinity, 0, -1, 2.01)) {
        Assert-Stop { Assert-QPFreshEngine (New-Observations) $fixture['identity'] $tolerance }
    }
    Assert-QPFreshEngine (New-Observations) $fixture['identity'] 0.5
    Assert-QPFreshEngine (New-Observations) $fixture['identity'] 2
}
Test-QPCase 'canonical numbers are edition-independent integers; fractions fail closed' {
    Assert-True ((ConvertTo-QPCanonicalJson (Read-QPJson '[1,1.0,1e2]')) -ceq '[1,1,100]') 'Numeric canonicalization differs'
    Assert-Stop { ConvertTo-QPCanonicalJson (Read-QPJson '[1e-5]') }
    Assert-Stop { ConvertTo-QPCanonicalJson (Read-QPJson '[0.00001]') }
}
Test-QPCase 'frozen body time fails despite advancing host/HTTP time' {
    $observations = New-Observations
    foreach ($o in $observations) {
        $o.system_time = $observations[0].system_time
        $body = Read-QPJson $o.capture.body; $body['SystemTime'] = $o.system_time
        $o.capture.body = ConvertTo-QPCanonicalJson $body
        $o.capture.raw = if ($o.transport -eq 'pipe') { "HTTP/1.0 200 OK`r`n`r`n$($o.capture.body)" } else { $o.capture.body }
        $o.capture.raw_base64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($o.capture.raw))
        $o.capture['http_date'] = $o.capture.start.utc
    }
    Assert-Stop { Assert-QPFreshEngine $observations $fixture['identity'] }
}
Test-QPCase 'HTTP framing preserves UTF-8 bytes and rejects incomplete bodies' {
    $body = '{"label":"synthetic-' + [char]0x00e9 + '"}'
    $bytes = [Text.Encoding]::UTF8.GetBytes($body)
    $raw = "HTTP/1.1 200 OK`r`nTransfer-Encoding: chunked`r`n`r`n" + $bytes.Length.ToString('x') + "`r`n$body`r`n0`r`n`r`n"
    $parsed = Read-QPHttpResponse ([Text.Encoding]::UTF8.GetBytes($raw))
    Assert-True ($parsed.body -ceq $body) 'UTF-8 chunk body changed'
    Assert-Stop { Read-QPHttpResponse ([Text.Encoding]::UTF8.GetBytes("HTTP/1.0 200 OK`r`nContent-Length: 999`r`n`r`n{}")) }
}
Test-QPCase 'both transports and fresh CLI processes are mandatory' {
    Assert-Stop { Assert-QPFreshEngine @((New-Observations)[0]) $fixture['identity'] }
    $o = New-Observations; $o[3].capture.pid = $o[1].capture.pid; $o[3].capture.process_start_utc = $o[1].capture.process_start_utc
    Assert-Stop { Assert-QPFreshEngine $o $fixture['identity'] }
    $o = New-Observations; $o[3].request_id = $o[1].request_id
    Assert-Stop { Assert-QPFreshEngine $o $fixture['identity'] }
}
Test-QPCase 'capture adapter pins context and uses distinct read-only transports (mocked)' {
    function Get-QPDockerOverrides { return @{} }
    function Invoke-QPReadOnlyCli {
        param($Executable, $Arguments, $WorkingDirectory)
        if ($Arguments -ceq 'context inspect synthetic-linux') {
            return @{ raw = '[{"Name":"synthetic-linux","Endpoints":{"docker":{"Host":"npipe:////./pipe/syntheticEngine"}}}]' }
        }
        if ($Arguments -cne '--context synthetic-linux info --format "{{json .}}"') { throw 'Unexpected CLI command' }
        return (New-Observations)[1].capture
    }
    function Invoke-QPReadOnlyPipe {
        param($PipeName)
        Assert-True ($PipeName -ceq 'syntheticEngine') 'Wrong pipe selected'
        return (New-Observations)[0].capture
    }
    $result = Get-QPEngineObservation (Join-Path $PSScriptRoot 'testdata\qualification-preservation\synthetic.json') 'synthetic-linux' 'npipe:////./pipe/syntheticEngine' $PSScriptRoot
    Assert-True ($result.Count -eq 2 -and $result[0].transport -ceq 'pipe' -and $result[1].transport -ceq 'cli') 'Independent transports missing'
    Assert-Stop { Get-QPEngineObservation 'unused' 'synthetic-linux' 'npipe:////./pipe/other' $PSScriptRoot }
    function Get-QPDockerOverrides { return @{ DOCKER_HOST = 'synthetic override' } }
    Assert-Stop { Get-QPEngineObservation 'unused' 'synthetic-linux' 'npipe:////./pipe/syntheticEngine' $PSScriptRoot }
}
Test-QPCase 'bounded native child capture and timeout need no Docker' {
    $shell = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    $capture = Invoke-QPReadOnlyCli $shell '-NoLogo -NoProfile -Command "Write-Output synthetic"' $PSScriptRoot 10000
    Assert-True ($capture.raw.Trim() -ceq 'synthetic' -and $capture.pid -ne $PID) 'Fresh native child not captured'
    Assert-Stop { Invoke-QPReadOnlyCli $shell '-NoLogo -NoProfile -Command "Start-Sleep -Seconds 30"' $PSScriptRoot 100 }
}
Test-QPCase 'a silent test-owned pipe times out without disposing a pending wait handle' {
    $name = 'qp-offline-' + [Guid]::NewGuid().ToString('N')
    $server = New-Object IO.Pipes.NamedPipeServerStream($name, [IO.Pipes.PipeDirection]::InOut, 1, [IO.Pipes.PipeTransmissionMode]::Byte, [IO.Pipes.PipeOptions]::Asynchronous)
    $accept = $server.WaitForConnectionAsync()
    try {
        Assert-Stop { Invoke-QPReadOnlyPipe $name 200 }
        Assert-True ($accept.Wait(1000)) 'Test pipe never connected'
        [Threading.Thread]::Sleep(50)
    } finally { $server.Dispose() }
}
Test-QPCase 'raw bytes and request provenance cannot be omitted or contradicted' {
    $o = New-Observations; $o[0].system_time = '2030-01-01T00:00:00Z'
    Assert-Stop { Assert-QPFreshEngine $o $fixture['identity'] }
    $o = New-Observations; $o[0].capture.raw = ''
    Assert-Stop { Assert-QPFreshEngine $o $fixture['identity'] }
    $o = New-Observations; $o[0].engine_id = 'unexpected-engine'
    Assert-Stop { Assert-QPFreshEngine $o $fixture['identity'] }
}
Test-QPCase 'one baseline and a complete unchanged final reconciliation pass' {
    $s = New-ReadySession
    Assert-QPPreservation $s (New-Inventory -StartTicks 11000) (New-Observations -StartTicks 11000) (New-Coverage) 21000 -Final
    Assert-True ($s.phase -ceq 'complete') 'Final reconciliation not complete'
    Assert-Stop { Set-QPWarmBaseline $s (New-Inventory) (New-Observations) 13000 }
    Assert-True ($s.phase -ceq 'complete' -and $null -eq $s.stop_reason) 'Terminal completion overwritten'
}
Test-QPCase 'prebaseline bridge replacement requires an exact authorized receipt' {
    $cold = New-Inventory; $warm = Copy-Fixture $cold
    $warm['networks'][0]['Id'] = 'bridge-after'; $warm['networks'][0]['Created'] = '2030-01-01T01:00:00Z'
    $s = New-QPPreservationSession $cold $fixture['identity'] ('c' * 64) 900
    Assert-Stop { Set-QPWarmBaseline $s $warm (New-Observations) 11000 }
    $s = New-QPPreservationSession $cold $fixture['identity'] ('c' * 64) 900
    $receipt = @{ authorization_sha256 = 'c' * 64; live_verified = $true; before_networks = $cold.networks; after_networks = $warm['networks'] }
    Set-QPWarmBaseline $s $warm (New-Observations) 11000 $receipt
    Assert-True ($s.phase -ceq 'observing') 'Authorized transition rejected'
    $warm['networks'][0]['Driver'] = 'unexpected'
    $s = New-QPPreservationSession $cold $fixture['identity'] ('c' * 64) 900
    Assert-Stop { Set-QPWarmBaseline $s $warm (New-Observations) 11000 $receipt }
}
Test-QPCase 'postbaseline bridge replacement stops even with identical configuration' {
    $s = New-ReadySession; $current = New-Inventory -StartTicks 11000; $current.networks[0]['Id'] = 'replacement'
    Assert-Stop { Assert-QPPreservation $s $current (New-Observations -StartTicks 11000) (New-Coverage) 21000 }
    Assert-True ($s.phase -ceq 'stopped') 'Drift not latched'
    $reason = $s.stop_reason
    Assert-Stop { Assert-QPPreservation $s (New-Inventory) (New-Observations) (New-Coverage) 12000 }
    Assert-True ($s.stop_reason -ceq $reason) 'Original failure reason overwritten'
}
Test-QPCase 'restart stops even when Engine ID and inventories are unchanged' {
    $s = New-ReadySession; $coverage = New-Coverage
    $coverage[2].events = @(@{ ticks = 11500; action = 'engine-starting'; object_id = 'same-engine' })
    Seal-Coverage $coverage
    Assert-Stop { Assert-QPPreservation $s (New-Inventory -StartTicks 11000) (New-Observations -StartTicks 11000) $coverage 21000 }
}
Test-QPCase 'coverage includes readiness and warm capture, not just baseline completion' {
    foreach ($kind in @(1, 2)) {
        $s = New-ReadySession; $coverage = New-Coverage
        $coverage[$kind].events = @(@{ ticks = 2000; action = 'restart-or-change'; object_id = 'bridge-before'; receipt_verified = $true })
        Seal-Coverage $coverage
        Assert-Stop { Assert-QPPreservation $s (New-Inventory -StartTicks 11000) (New-Observations -StartTicks 11000) $coverage 21000 -Final }
    }
    Assert-Stop { New-QPPreservationSession (New-Inventory) $fixture['identity'] ('c' * 64) }
}
Test-QPCase 'batched transports use earliest request and latest completion, not array position' {
    $inventory = New-Inventory; $o = New-Observations
    $grouped = @($o[1], $o[3], $o[0], $o[2]) # CLI pair, then earlier pipe pair
    $s = New-QPPreservationSession $inventory $fixture['identity'] ('c' * 64) 900
    Set-QPWarmBaseline $s $inventory $grouped 11000
    Assert-True ($s.phase -ceq 'observing') 'Valid transport batching rejected'
    $s = New-QPPreservationSession $inventory $fixture['identity'] ('c' * 64) 2000
    Assert-Stop { Set-QPWarmBaseline $s $inventory $grouped 11000 }
    Assert-True ($s.stop_reason -match 'observation starts after') 'Earliest readiness gap not detected'
    $s = New-QPPreservationSession $inventory $fixture['identity'] ('c' * 64) 900
    Assert-Stop { Set-QPWarmBaseline $s $inventory $grouped 8000 }
    Assert-True ($s.stop_reason -match 'freshness gate expired or clock inconsistent') 'Future completion hidden by array order'
}
Test-QPCase 'untimed or ambiguous events are never skipped as out of window' {
    foreach ($ticks in @($null, 'not-a-clock', '11500', 11500.5, -1)) {
        $coverage = New-Coverage
        $coverage[2].events = @(@{ ticks = $ticks; action = 'engine-starting' })
        Assert-Stop { Seal-Coverage $coverage; Assert-QPObservationCoverage $coverage 900 12000 (New-Inventory) }
    }
    $coverage = New-Coverage; $coverage[2].events = @(@{ action = 'engine-starting' })
    Seal-Coverage $coverage
    Assert-Stop { Assert-QPObservationCoverage $coverage 900 12000 (New-Inventory) }
    $coverage = New-Coverage; [void]$coverage[0].Remove('start_ticks')
    Seal-Coverage $coverage
    Assert-Stop { Assert-QPObservationCoverage $coverage 900 12000 (New-Inventory) }
}
Test-QPCase 'public clock arguments are validated without binding-time rounding' {
    Assert-Stop { New-QPPreservationSession (New-Inventory) $fixture['identity'] ('c' * 64) 900.5 }
    Assert-Stop { Assert-QPObservationCoverage (New-Coverage) 900.5 12000 (New-Inventory) }
    Assert-Stop { Assert-QPObservationCoverage (New-Coverage) 900 12000.5 (New-Inventory) }
    $s = New-QPPreservationSession (New-Inventory) $fixture['identity'] ('c' * 64) 900
    Assert-Stop { Set-QPWarmBaseline $s (New-Inventory) (New-Observations) 11000.5 }
    Assert-Stop { Assert-QPPreservation (New-ReadySession) (New-Inventory -StartTicks 11000) (New-Observations -StartTicks 11000) (New-Coverage) 21000.5 }
}
Test-QPCase 'matching inventories cannot compensate for missing/gapped/interrupted observers' {
    foreach ($kind in @('container', 'network', 'lifecycle')) {
        $allStreams = New-Coverage
        $coverage = @($allStreams | Where-Object { $_.kind -cne $kind })
        Assert-Stop { Assert-QPPreservation (New-ReadySession) (New-Inventory -StartTicks 11000) (New-Observations -StartTicks 11000) $coverage 21000 }
    }
    foreach ($reason in @('EOF', 'deadline', 'error')) {
        $coverage = New-Coverage; $coverage[0].exit_reason = $reason
        Seal-Coverage $coverage
        Assert-Stop { Assert-QPObservationCoverage $coverage 11000 12000 }
    }
    $coverage = New-Coverage; $coverage[1].gaps = @(@{ start = 11200; end = 11400 })
    Seal-Coverage $coverage
    Assert-Stop { Assert-QPObservationCoverage $coverage 11000 12000 }
    $coverage = New-Coverage; $coverage[2].end_ticks = 11999
    Seal-Coverage $coverage
    Assert-Stop { Assert-QPObservationCoverage $coverage 11000 12000 }
}
Test-QPCase 'unattributed network/container events stop' {
    foreach ($i in @(0, 1)) {
        $coverage = New-Coverage; $coverage[$i].events = @(@{ ticks = 11500; object_id = 'unknown'; receipt_verified = $false })
        Seal-Coverage $coverage
        Assert-Stop { Assert-QPObservationCoverage $coverage 11000 12000 (New-Inventory) }
    }
}
Test-QPCase 'observer provenance rejects placeholder identities and invalid UTC' {
    foreach ($change in @(
        @{ key = 'pid'; value = 0 }, @{ key = 'pid'; value = '201' },
        @{ key = 'process_start_utc'; value = '' }, @{ key = 'process_start_utc'; value = '2030-02-30T00:00:00Z' },
        @{ key = 'process_start_utc'; value = '2030-01-01T00:00:00' },
        @{ key = 'executable_sha256'; value = 'x' }, @{ key = 'source'; value = ' ' }
    )) {
        $coverage = New-Coverage; $coverage[0].provenance[$change.key] = $change.value
        Seal-Coverage $coverage
        Assert-Stop { Assert-QPObservationCoverage $coverage 900 12000 (New-Inventory) }
    }
}
Test-QPCase 'receipt assertions cannot excuse events on a protected object' {
    $coverage = New-Coverage
    $coverage[1].events = @(@{ ticks = 11500; object_id = 'bridge-before'; receipt_verified = $true })
    Seal-Coverage $coverage
    Assert-Stop { Assert-QPObservationCoverage $coverage 11000 12000 (New-Inventory) }
    $coverage[1].events[0].object_id = 'receipt-owned-new-object'
    Seal-Coverage $coverage
    Assert-QPObservationCoverage $coverage 11000 12000 (New-Inventory)
}
Test-QPCase 'approved bridge attachment passes only after raw receipt and endpoint reconciliation' {
    $c = New-BridgeFixture; $before = ConvertTo-QPCanonicalJson $c.coverage
    Assert-BridgeFixture $c
    Assert-True ($c.session.phase -ceq 'complete') 'Complete attachment reconciliation rejected'
    Assert-True ((ConvertTo-QPCanonicalJson $c.coverage) -ceq $before) 'Raw Docker event was rewritten'
    $c = New-BridgeFixture
    $c.proof.receipts[0].inspection = New-AttachmentSource @((Read-QPJson $c.proof.receipts[0].inspection.raw)) 11150
    foreach ($transition in $c.proof.transitions) {
        $transition.network_inspection = New-AttachmentSource @((Read-QPJson $transition.network_inspection.raw)) $transition.network_inspection.ticks
    }
    Assert-BridgeFixture $c # Preserve ordinary one-element CLI inspect arrays.
}
Test-QPCase 'foreign or flag-only bridge ownership cannot pass' {
    foreach ($change in @(
        { param($c) $c.proof = $null },
        { param($c) $c.proof.receipts = @() },
        { param($c) $e = $c.coverage[1].events[0]; $raw = Read-QPJson $e.docker_source.raw; $raw['Actor']['Attributes']['container'] = 'd' * 64; $e.docker_source = New-AttachmentSource $raw 11200; $c.proof.transitions[0].event_sha256 = $e.docker_source.raw_sha256 },
        { param($c) $c.session.test_container_plans = @() },
        { param($c) $c.proof.receipts += $c.proof.receipts[0] },
        { param($c) $c.proof.receipts[0].create_event_sha256 = '0' * 64 },
        { param($c) $v = Read-QPJson $c.proof.receipts[0].inspection.raw; $v['Image'] = 'sha256:' + ('d' * 64); $c.proof.receipts[0].inspection = New-AttachmentSource $v 11150 },
        { param($c) $v = Read-QPJson $c.proof.receipts[0].inspection.raw; $v['Config']['Cmd'] = @('foreign'); $c.proof.receipts[0].inspection = New-AttachmentSource $v 11150 },
        { param($c) $v = Read-QPJson $c.proof.receipts[0].inspection.raw; $v['Mounts'][0]['Source'] = 'C:\Foreign'; $c.proof.receipts[0].inspection = New-AttachmentSource $v 11150 },
        { param($c) $v = Read-QPJson $c.proof.receipts[0].inspection.raw; $v['Created'] = '2031-01-01T00:00:00Z'; $c.proof.receipts[0].inspection = New-AttachmentSource $v 11150 },
        { param($c) $c.proof.authorization_sha256 = '0' * 64 },
        { param($c) $c.proof.engine = @{ engine_id = 'foreign' } }
    )) { $c = New-BridgeFixture; & $change $c; Assert-Stop { Assert-BridgeFixture $c }; Assert-True ($c.session.phase -ceq 'stopped') 'Ownership failure not latched' }
}
Test-QPCase 'protected bridge destruction replacement and configuration drift remain STOP' {
    foreach ($change in @(
        { param($c) $e = $c.coverage[1].events[0]; $raw = Read-QPJson $e.docker_source.raw; $raw['Action'] = 'destroy'; $e.action = 'destroy'; $e.docker_source = New-AttachmentSource $raw 11200; $c.proof.transitions[0].event_sha256 = $e.docker_source.raw_sha256 },
        { param($c) $c.current['networks'][0]['Id'] = 'replaced' },
        { param($c) $v = Read-QPJson $c.proof.transitions[0].network_inspection.raw; $v['Created'] = '2031-01-01T00:00:00Z'; $c.proof.transitions[0].network_inspection = New-AttachmentSource $v 11250 },
        { param($c) $v = Read-QPJson $c.proof.transitions[0].network_inspection.raw; $v['Options']['synthetic'] = 'changed'; $c.proof.transitions[0].network_inspection = New-AttachmentSource $v 11250 }
    )) { $c = New-BridgeFixture; & $change $c; Assert-Stop { Assert-BridgeFixture $c } }
}
Test-QPCase 'unmatched or changed actual endpoints cannot be reconciled away' {
    foreach ($change in @(
        { param($c) $c.coverage[1].events = @($c.coverage[1].events[0]); $c.proof.transitions = @($c.proof.transitions[0]) },
        { param($c) $c.coverage[1].events = @($c.coverage[1].events[1]); $c.proof.transitions = @($c.proof.transitions[1]) },
        { param($c) $c.current['networks'][0]['Containers'][$c.cid] = @{ EndpointID = 'f' * 64 } },
        { param($c) $v = Read-QPJson $c.proof.transitions[0].network_inspection.raw; [void]$v['Containers'].Remove($c.cid); $c.proof.transitions[0].network_inspection = New-AttachmentSource $v 11250 },
        { param($c) $v = Read-QPJson $c.proof.transitions[0].network_inspection.raw; $v['Containers']['b' * 64]['EndpointID'] = 'd' * 64; $c.proof.transitions[0].network_inspection = New-AttachmentSource $v 11250 },
        { param($c) $v = Read-QPJson $c.proof.transitions[0].network_inspection.raw; $v['Containers'][$c.cid]['EndpointID'] = 'c' * 64; $c.proof.transitions[0].network_inspection = New-AttachmentSource $v 11250 },
        { param($c) $c.proof.transitions[0].network_inspection.ticks = 11350 },
        { param($c) $c.proof.transitions[1].network_inspection = $c.proof.transitions[0].network_inspection; $c.proof.transitions[1].network_inspection.ticks = 11350 }
    )) { $c = New-BridgeFixture; & $change $c; Assert-Stop { Assert-BridgeFixture $c } }
}
Test-QPCase 'bridge evidence must retain unambiguous original Docker event identity' {
    foreach ($change in @(
        { param($c) [void]$c.coverage[1].events[0].Remove('docker_source') },
        { param($c) $c.coverage[1].events[0].docker_source.raw_sha256 = '0' * 64 },
        { param($c) $e = $c.coverage[1].events[0]; $raw = Read-QPJson $e.docker_source.raw; $raw['Actor']['ID'] = 'substituted'; $e.docker_source = New-AttachmentSource $raw 11200 },
        { param($c) $e = $c.coverage[1].events[0]; $raw = Read-QPJson $e.docker_source.raw; [void]$raw['Actor']['Attributes'].Remove('container'); $e.docker_source = New-AttachmentSource $raw 11200 },
        { param($c) $e = $c.coverage[1].events[0]; $raw = $e.docker_source.raw.Replace('"container":', '"container":"foreign","container":'); $e.docker_source = @{ raw = $raw; raw_sha256 = Get-QPSha256 $raw; ticks = 11200 } },
        { param($c) $c.proof.transitions += $c.proof.transitions[0] },
        { param($c) $c.coverage[0].events = @() },
        { param($c) $c.coverage[0].events += $c.coverage[0].events[0] }
    )) { $c = New-BridgeFixture; & $change $c; Assert-Stop { Assert-BridgeFixture $c } }
}
Test-QPCase 'raw Docker identity is checked before generic event classification' {
    foreach ($change in @(
        { param($c) foreach ($e in $c.coverage[1].events) { $e.object_id = 'unprotected-test-object' }; $c.proof = $null },
        { param($c) $c.coverage[0].events[0].object_id = 'unprotected-test-object' },
        { param($c) $c.coverage[0].events[0].action = 'substituted' },
        { param($c) $e = $c.coverage[0].events[0]; $raw = Read-QPJson $e.docker_source.raw; $raw['Type'] = 'network'; $e.docker_source = New-AttachmentSource $raw $e.ticks },
        { param($c) $c.coverage[0].events[0].docker_source.ticks++ }
    )) {
        $c = New-BridgeFixture; & $change $c
        Assert-Stop { Assert-BridgeFixture $c }
        Assert-True ($c.session.phase -ceq 'stopped') 'Contradictory source failure not latched'
    }
}
Test-QPCase 'bridge authorization never substitutes for uninterrupted observation coverage' {
    foreach ($change in @(
        { param($c) $c.coverage = @($c.coverage[0], $c.coverage[1]) },
        { param($c) $c.coverage[1].complete = $false; $c.coverage[1].exit_reason = 'EOF' },
        { param($c) $c.coverage[2].gaps = @(@{ reason = 'log rotation' }) },
        { param($c) $c.coverage[0].start_ticks = 11200 }
    )) { $c = New-BridgeFixture; & $change $c; Assert-Stop { Assert-BridgeFixture $c } }
}
Test-QPCase 'inventory capture must be freshness-bracketed and old observations cannot be reused' {
    $s = New-ReadySession; $current = New-Inventory -StartTicks 11000
    $current.capture_window.end_ticks = 20500
    Assert-Stop { Assert-QPPreservation $s $current (New-Observations -StartTicks 11000) (New-Coverage) 21000 }
    $inventory = New-Inventory; $o = New-Observations
    $s = New-QPPreservationSession $inventory $fixture['identity'] ('c' * 64) 900
    Set-QPWarmBaseline $s $inventory $o 11000
    Assert-Stop { Assert-QPPreservation $s $inventory $o (New-Coverage) 11000 }
}
Test-QPCase 'missing raw PATH prevents even baseline acceptance' {
    $inventory = New-Inventory
    [void]$inventory.protected_host.path.paths.process.Remove('raw')
    $s = New-QPPreservationSession $inventory $fixture['identity'] ('c' * 64) 900
    Assert-Stop { Set-QPWarmBaseline $s $inventory (New-Observations) 11000 }
}
Test-QPCase 'raw PATH retains duplicates, empties and whitespace' {
    $p = New-PathFixture
    Assert-True ($p.paths.process.segments.Count -eq 5) 'Empty segments lost'
    Assert-True ($p.paths.process.segments[1] -ceq '') 'Empty segment lost'
    Assert-True ($p.paths.process.segments[2] -ceq ' C:\Synthetic\Other ') 'Whitespace lost'
    Assert-QPPathPreserved $p (Copy-Fixture $p)
}
Test-QPCase 'PATH capture UTC and process scope provenance are mandatory' {
    $p = New-PathFixture; [void]$p.Remove('captured_utc')
    Assert-Stop { Assert-QPPathPreserved $p $p }
    foreach ($stamp in @('', 'not-a-date', '2030-02-30T00:00:00Z', '2030-01-01T00:00:00')) {
        $p = New-PathFixture; $p.captured_utc = $stamp
        Assert-Stop { Assert-QPPathPreserved $p $p }
    }
    $p = New-PathFixture; $p.paths.process.registry_kind = 'String'
    Assert-Stop { Assert-QPPathPreserved $p $p }
    $p = New-PathFixture; $p.context.parent.start_utc = ''
    Assert-Stop { Assert-QPPathPreserved $p $p }
}
Test-QPCase 'absent and present-empty PATH have distinct canonical representations' {
    $absent = New-QPPathValue $null $false 'absent'
    $empty = New-QPPathValue '' $true 'String'
    Assert-True ($null -eq $absent.raw -and $null -eq $absent.sha256 -and $absent.segments.Count -eq 0) 'Absent PATH misrepresented'
    Assert-True ($empty.raw -ceq '' -and $empty.segments.Count -eq 1) 'Empty PATH misrepresented'
    $before = New-PathFixture; $before.paths.user = $absent
    Assert-QPPathPreserved $before (Copy-Fixture $before)
    $after = Copy-Fixture $before; $after['paths']['user'] = $empty
    Assert-Stop { Assert-QPPathPreserved $before $after }
}
Test-QPCase 'non-string registry PATH is rejected without coercion' {
    $values = New-Object 'System.Collections.Generic.List[object]'
    $values.Add([string[]]@('C:\Synthetic\A', 'C:\Synthetic\B'))
    $values.Add([byte[]]@(1, 2, 3))
    $values.Add([int]7); $values.Add([long]9)
    foreach ($value in $values) {
        Assert-Stop { New-QPPathValue -Raw $value -Present $true -RegistryKind 'String' }
    }
    foreach ($kind in @('MultiString', 'Binary', 'DWord', 'QWord', 'None')) {
        Assert-Stop { New-QPPathValue -Raw 'fabricated text' -Present $true -RegistryKind $kind }
    }
    Assert-Stop { New-QPPathValue -Raw $null -Present $true -RegistryKind 'String' }
    $p = New-PathFixture; $p.paths.user.registry_kind = 'process'
    Assert-Stop { Assert-QPPathPreserved $p $p }
}
Test-QPCase 'PATH reordering, duplicates, empty entries and whitespace remain meaningful' {
    foreach ($raw in @('B;A', 'A;B', 'A;A;B', 'A;;B', 'A; B', 'A;B;')) {
        $before = New-PathFixture; $after = Copy-Fixture $before
        $before.paths.process = New-QPPathValue 'A;B' $true 'process'
        $after['paths']['process'] = New-QPPathValue $raw $true 'process'
        if ($raw -ceq 'A;B') { Assert-QPPathPreserved $before $after }
        else { Assert-Stop { Assert-QPPathPreserved $before $after } }
    }
}
Test-QPCase 'missing raw historical PATH or different launcher cannot pass' {
    $before = New-PathFixture; $after = Copy-Fixture $before
    [void]$after['paths']['process'].Remove('raw')
    Assert-Stop { Assert-QPPathPreserved $before $after }
    $after = Copy-Fixture $before; $after['context']['parent']['pid'] = 98
    Assert-Stop { Assert-QPPathPreserved $before $after }
    $after = Copy-Fixture $before; $after['paths']['machine'] = New-QPPathValue 'different' $true 'ExpandString'
    Assert-Stop { Assert-QPPathPreserved $before $after }
}
Test-QPCase 'intentional child overlays do not mutate or excuse parent PATH' {
    $base = @{ PATH = 'A;B'; KEEP = 'yes' }; $overlay = @{ PATH = 'Private;A;B'; KEEP = $null }
    $child = Get-QPChildEnvironment $base $overlay
    Assert-True ($child.PATH -ceq 'Private;A;B' -and -not $child.ContainsKey('KEEP')) 'Child recipe wrong'
    Assert-True ($base.PATH -ceq 'A;B' -and $base.KEEP -ceq 'yes') 'Parent dictionary mutated'
    $before = New-PathFixture; $after = Copy-Fixture $before
    $after['paths']['process'] = New-QPPathValue $child.PATH $true 'process'
    Assert-Stop { Assert-QPPathPreserved $before $after }
}
Test-QPCase 'sealed records are append-only and hash-bound' {
    # Synthetic ACL adapter: sandbox/runner TEMP permissions vary. Exercise the
    # writer's policy without changing any real ACL or writing private host data.
    function Get-Acl {
        param([string]$LiteralPath)
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $acl = New-Object Security.AccessControl.DirectorySecurity
        $acl.SetOwner($sid)
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, [Security.AccessControl.AccessControlType]::Allow)))
        return $acl
    }
    $root = Join-Path ([IO.Path]::GetTempPath()) ('qp-offline-' + [Guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($root)
    try {
        $record = Write-QPPrivateEvidence $root 'synthetic.json' @{ result = 'FAIL'; synthetic = $true } ('0' * 64)
        $path = Join-Path $root 'synthetic.json'
        Assert-True ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $record.sha256) 'Written hash differs'
        $failed = $false
        try { Write-QPPrivateEvidence $root 'synthetic.json' @{ result = 'PASS' } $record.sha256 | Out-Null } catch { $failed = $true }
        Assert-True $failed 'Sealed evidence overwritten'
        Assert-True ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $record.sha256) 'FAIL record changed'
        Assert-QPPrivateEvidenceChain $root @($record) $record.sha256
        Assert-Stop { Assert-QPPrivateEvidenceChain $root @($record) ('f' * 64) }
        $second = Write-QPPrivateEvidence $root 'second.json' @{ result = 'STOP'; synthetic = $true } $record.sha256
        Assert-QPPrivateEvidenceChain $root @($record, $second) $second.sha256
        Assert-Stop { Assert-QPPrivateEvidenceChain $root @($second, $record) $record.sha256 }
        $bad = Write-QPPrivateEvidence $root 'bad.json' @{ synthetic = $true } ('0' * 64)
        Assert-Stop { Assert-QPPrivateEvidenceChain $root @($record, $bad) $bad.sha256 }
    } finally {
        # Exact files created by this test only. No recursive/prefix deletion.
        foreach ($name in @('synthetic.json', 'second.json', 'bad.json')) {
            if ([IO.File]::Exists((Join-Path $root $name))) { [IO.File]::Delete((Join-Path $root $name)) }
        }
        [IO.Directory]::Delete($root)
    }
}
Test-QPCase 'broad private-directory access is rejected without changing ACLs' {
    function Get-Acl {
        param([string]$LiteralPath)
        $sid = New-Object Security.Principal.SecurityIdentifier('S-1-1-0')
        $acl = New-Object Security.AccessControl.DirectorySecurity
        $acl.SetOwner([Security.Principal.WindowsIdentity]::GetCurrent().User)
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::Read, [Security.AccessControl.AccessControlType]::Allow)))
        return $acl
    }
    Assert-Stop { Write-QPPrivateEvidence ([IO.Path]::GetTempPath()) 'must-not-write.json' @{ synthetic = $true } ('0' * 64) }
}
Test-QPCase 'explicit Administrators access is rejected' {
    function Get-Acl {
        param([string]$LiteralPath)
        $acl = New-Object Security.AccessControl.DirectorySecurity
        $acl.SetOwner([Security.Principal.WindowsIdentity]::GetCurrent().User)
        $sid = New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::Read, [Security.AccessControl.AccessControlType]::Allow)))
        return $acl
    }
    Assert-Stop { Write-QPPrivateEvidence ([IO.Path]::GetTempPath()) 'must-not-write.json' @{ synthetic = $true } ('0' * 64) }
}
Test-QPCase 'pinned evidence directory cannot be renamed between validation and write' {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('qp-pin-offline-' + [Guid]::NewGuid().ToString('N'))
    $moved = $root + '-moved'
    [void][IO.Directory]::CreateDirectory($root)
    $script:pinBlocked = $false
    function Get-Acl {
        param([string]$LiteralPath)
        try { [IO.Directory]::Move($root, $moved) } catch [IO.IOException] { $script:pinBlocked = $true }
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $acl = New-Object Security.AccessControl.DirectorySecurity
        $acl.SetOwner($sid)
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, [Security.AccessControl.AccessControlType]::Allow)))
        return $acl
    }
    try {
        Write-QPPrivateEvidence $root 'synthetic.json' @{ synthetic = $true } ('0' * 64) | Out-Null
        Assert-True $script:pinBlocked 'Directory replacement was not blocked'
        Assert-True (-not [IO.Directory]::Exists($moved)) 'Evidence directory moved'
    } finally {
        foreach ($owned in @($root, $moved)) {
            if ([IO.Directory]::Exists($owned)) {
                $file = Join-Path $owned 'synthetic.json'
                if ([IO.File]::Exists($file)) { [IO.File]::Delete($file) }
                [IO.Directory]::Delete($owned)
            }
        }
    }
}
Write-Output "Offline preservation suite: $script:passed cases passed (PowerShell $($PSVersionTable.PSVersion)); no Docker/network calls."
