# TFG OS usage reporter for Windows 10/11. Run in your own Windows PowerShell.
[CmdletBinding()]
param([string]$OsUrl, [string]$LaptopToken, [switch]$Remove)
$ErrorActionPreference = 'Stop'
$TaskName = 'TFG OS Usage ' + [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$Dir = Join-Path $env:LOCALAPPDATA 'TFG-OS\usage'

if ($Remove) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    if (Test-Path $Dir) { Remove-Item -Recurse -Force $Dir }
    Write-Host 'Removed. This laptop no longer reports usage.'
    return
}
if ($env:OS -ne 'Windows_NT') { throw 'This installer is for Windows.' }
if (-not $LaptopToken -or -not $OsUrl) { throw 'Copy the complete Windows command from TFG OS > Usage > Connect this laptop.' }
$Base = [uri]$OsUrl
if ($Base.Scheme -ne 'https' -or $Base.UserInfo -or $Base.Query -or $Base.Fragment) { throw 'The OS address must be an HTTPS URL.' }
$OsUrl = $OsUrl.TrimEnd('/')
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$Arch = $env:PROCESSOR_ARCHITEW6432
if (-not $Arch) { $Arch = $env:PROCESSOR_ARCHITECTURE }
switch ($Arch.ToUpperInvariant()) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' { $Arch = 'arm64' }
    default { throw 'A 64-bit Windows PC (Intel/AMD or ARM) is required.' }
}
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
# Restrict the install to this Windows user and SYSTEM; no admin rights needed.
$Acl = New-Object Security.AccessControl.DirectorySecurity
$Acl.SetAccessRuleProtection($true, $false)
$Sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
$Acl.SetOwner($Sid)
foreach ($Principal in @($Sid, [Security.Principal.SecurityIdentifier]'S-1-5-18')) {
    $Rule = New-Object Security.AccessControl.FileSystemAccessRule($Principal, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $Acl.AddAccessRule($Rule)
}
Set-Acl -Path $Dir -AclObject $Acl
$Exe = Join-Path $Dir 'tfg-usage.exe'
$Download = Join-Path $Dir 'tfg-usage.new.exe'
Write-Host 'Downloading the usage reporter...'
try {
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Headers @{Authorization = "Bearer $LaptopToken"} -Uri "$OsUrl/api/founderos/laptop/collector/windows-$Arch" -OutFile $Download
    $Bytes = [IO.File]::ReadAllBytes($Download)
    if ($Bytes.Length -lt 2 -or $Bytes[0] -ne 77 -or $Bytes[1] -ne 90) { throw 'The server did not return a Windows reporter.' }
    Move-Item -Force $Download $Exe
} finally {
    if (Test-Path $Download) { Remove-Item -Force $Download }
}
# DPAPI encrypts the token for this user on this PC. It is never a task argument.
$EncryptedToken = ConvertTo-SecureString -String $LaptopToken -AsPlainText -Force | ConvertFrom-SecureString
@{ url = "$OsUrl/api/founderos/laptop/push"; token = $EncryptedToken } | ConvertTo-Json | Set-Content -Encoding UTF8 (Join-Path $Dir 'config.json')
$LaptopToken = $null
$Runner = @'
$ErrorActionPreference = 'Stop'
$Config = Get-Content -Raw (Join-Path $PSScriptRoot 'config.json') | ConvertFrom-Json
$SecureToken = ConvertTo-SecureString $Config.token
$Credential = New-Object System.Management.Automation.PSCredential('laptop', $SecureToken)
$Previous = $env:FOUNDEROS_COLLECTOR_TOKEN
try {
    $env:FOUNDEROS_COLLECTOR_TOKEN = $Credential.GetNetworkCredential().Password
    & (Join-Path $PSScriptRoot 'tfg-usage.exe') -usage-only -url $Config.url -deadline 120s 2>&1 | Out-File -Encoding UTF8 (Join-Path $PSScriptRoot 'last-run.log')
    if ($LASTEXITCODE -ne 0) { throw "Usage upload failed ($LASTEXITCODE). See last-run.log in $PSScriptRoot." }
} finally {
    $env:FOUNDEROS_COLLECTOR_TOKEN = $Previous
}
'@
$RunnerPath = Join-Path $Dir 'report.ps1'
Set-Content -Encoding UTF8 -Path $RunnerPath -Value $Runner
# Current-user interactive task: runs while signed in, never as administrator.
$PowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$Arguments = '-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $RunnerPath + '"'
$Action = New-ScheduledTaskAction -Execute $PowerShell -Argument $Arguments
$Trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(10) -RepetitionInterval (New-TimeSpan -Minutes 10)
$Principal = New-ScheduledTaskPrincipal -UserId $Sid.Value -LogonType Interactive -RunLevel Limited
$Settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 3)
Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Principal $Principal -Settings $Settings -Description 'Report Claude Code and Codex usage numbers to TFG OS every 10 minutes.' -Force | Out-Null
try {
    & $PowerShell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $RunnerPath
    if ($LASTEXITCODE -ne 0) { throw 'The first upload did not succeed.' }
    Write-Host 'Connected. Refresh TFG OS > Usage. Updates run every 10 minutes while you are signed in.'
} catch {
    Write-Warning 'Reporter installed; the first upload failed. Check last-run.log in the install folder. The scheduled task will retry in 10 minutes.'
}
Write-Host 'Only Claude Code and Codex usage numbers are uploaded. No chats, files or notes.'
Write-Host "Install folder: $Dir"
