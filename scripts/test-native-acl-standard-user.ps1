# Disposable GitHub-hosted runner fixture, authorized for C1 qualification.
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted' -or $env:RUNNER_OS -ne 'Windows') {
    throw 'Standard-user ACL qualification is restricted to disposable GitHub-hosted Windows runners.'
}

$projectRoot = Split-Path -Parent $PSScriptRoot
$stageName = 'cb-acl-ci-' + [guid]::NewGuid().ToString('N')
$stageRoot = [IO.Path]::GetFullPath((Join-Path ($env:SystemDrive + '\') $stageName))
if ((Split-Path -Leaf $stageRoot) -ne $stageName -or (Test-Path -LiteralPath $stageRoot)) {
    throw 'Unsafe or pre-existing qualification staging directory.'
}
$accountName = 'cbacl' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$createdSID = $null
$process = $null
$stageCreated = $false
$cleanupErrors = [Collections.Generic.List[string]]::new()
try {
    New-Item -ItemType Directory -Path $stageRoot | Out-Null
    $stageCreated = $true
    $testExe = Join-Path $stageRoot 'aclaudit.test.exe'
    Push-Location -LiteralPath $projectRoot
    try {
        & go test -c -o $testExe ./internal/aclaudit
        if ($LASTEXITCODE -ne 0) { throw 'Native ACL test compilation failed.' }
    } finally { Pop-Location }

    $passwordBytes = [byte[]]::new(48)
    [Security.Cryptography.RandomNumberGenerator]::Fill($passwordBytes)
    $password = [Convert]::ToBase64String($passwordBytes) + 'aA1!'
    Write-Host "::add-mask::$password"
    $securePassword = ConvertTo-SecureString -String $password -AsPlainText -Force
    $password = $null
    $account = New-LocalUser -Name $accountName -Password $securePassword -Description 'Disposable ContainerBin native ACL CI fixture' -AccountNeverExpires
    $createdSID = $account.SID
    $usersSID = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-545')
    if (-not (Get-LocalGroupMember -SID $usersSID | Where-Object { $_.SID -eq $createdSID })) {
        Add-LocalGroupMember -SID $usersSID -Member $account
    }
    if (Get-LocalGroupMember -SID ([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')) | Where-Object { $_.SID -eq $createdSID }) {
        throw 'Qualification account unexpectedly belongs to Administrators.'
    }

    # Grant only read/execute on this new staging tree to the new account.
    # No existing folder, drive, profile or machine ACL is modified.
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    $propagation = [Security.AccessControl.PropagationFlags]::None
    $allow = [Security.AccessControl.AccessControlType]::Allow
    $runnerSID = [Security.Principal.WindowsIdentity]::GetCurrent().User
    foreach ($sid in @($runnerSID, [Security.Principal.SecurityIdentifier]::new('S-1-5-18'), [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', $inheritance, $propagation, $allow))
    }
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($createdSID, 'ReadAndExecute', $inheritance, $propagation, $allow))
    Set-Acl -LiteralPath $stageRoot -AclObject $acl

    $credential = [pscredential]::new("$env:COMPUTERNAME\$accountName", $securePassword)
    $stdout = Join-Path $stageRoot 'stdout.txt'
    $stderr = Join-Path $stageRoot 'stderr.txt'
    $process = Start-Process -FilePath $testExe -ArgumentList '-test.run=^TestNative', '-test.v', '-test.timeout=90s' -Credential $credential -LoadUserProfile -WorkingDirectory $stageRoot -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr -Environment @{
        CB_ACL_STANDARD_USER = '1'
        CB_ACL_EXPECTED_SID = $createdSID.Value
        CB_ACL_AUDIT_CHILD = $null
        CB_ACL_EXPECT_INCOMPLETE = $null
    } -PassThru
    $null = $process.Handle
    if (-not $process.WaitForExit(120000)) {
        Stop-Process -InputObject $process -Force
        throw 'Standard-user native ACL qualification timed out.'
    }
    $process.WaitForExit()
    Get-Content -LiteralPath $stdout
    Get-Content -LiteralPath $stderr
    if ($process.ExitCode -ne 0) { throw "Standard-user native ACL qualification failed (exit $($process.ExitCode))." }
} finally {
    if ($null -ne $process -and -not $process.HasExited) {
        try { Stop-Process -InputObject $process -Force } catch { $cleanupErrors.Add('stop owned qualification child: ' + $_.Exception.Message) }
    }
    if ($null -ne $createdSID) {
        try {
            $profilesRoot = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables((Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList').ProfilesDirectory)).TrimEnd('\')
            $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$($createdSID.Value)'"
            if ($null -ne $profile) {
                $profilePath = [IO.Path]::GetFullPath($profile.LocalPath).TrimEnd('\')
                $leaf = Split-Path -Leaf $profilePath
                if ($profile.Loaded -or $profile.Special -or (Split-Path -Parent $profilePath) -ne $profilesRoot -or ($leaf -ne $accountName -and -not $leaf.StartsWith($accountName + '.', [StringComparison]::OrdinalIgnoreCase))) {
                    throw 'Refusing cleanup of unqualified or loaded user profile.'
                }
                $profile | Remove-CimInstance
                if (Test-Path -LiteralPath $profilePath) { throw 'Owned qualification profile directory remains after cleanup.' }
            }
        } catch { $cleanupErrors.Add('remove owned qualification profile: ' + $_.Exception.Message) }
        try {
            Remove-LocalUser -SID $createdSID
            if (Get-LocalUser -SID $createdSID -ErrorAction SilentlyContinue) { throw 'Owned qualification account remains after cleanup.' }
        } catch { $cleanupErrors.Add('remove owned qualification account: ' + $_.Exception.Message) }
    }
    if ($stageCreated) {
        try {
            $resolvedStage = [IO.Path]::GetFullPath($stageRoot)
            if ((Split-Path -Leaf $resolvedStage) -ne $stageName -or (Split-Path -Parent $resolvedStage).TrimEnd('\') -ne $env:SystemDrive) {
                throw 'Refusing cleanup outside the exact new staging tree.'
            }
            Remove-Item -LiteralPath $resolvedStage -Recurse -Force
        } catch { $cleanupErrors.Add('remove owned qualification staging: ' + $_.Exception.Message) }
    }
    if ($cleanupErrors.Count -ne 0) { throw ($cleanupErrors -join '; ') }
    if ($null -ne $createdSID) { Write-Host 'Removed disposable standard-user account, profile and staging tree.' }
}
