<#
.SYNOPSIS
    Keeps Watcher Pinger for Windows and the network it pings up to date in one folder: a shared folder of a file
    server the technicians take them from, or a folder on a terminal server they run the pinger from. Run it by hand or
    from the Task Scheduler.

.DESCRIPTION
    Takes watcher-pinger.exe of the latest release from GitHub, checked against its SHA256SUMS, and the network from the
    API of Watcher NMS into watcher-pinger.json beside it. A file that does not check out is not put in place, and the
    one there stays.

    A pinger running keeps running: its program is moved aside and the new one is taken at its next start. The new
    network is taken by Reload data on the page.

    The network needs the API key of a Watcher NMS user of the role watcher-pinger, kept by -SetToken in a file only
    SYSTEM and the administrators read, and the address of this computer in PINGER_API_ALLOWED_ADDRESSES of Watcher NMS
    where that is set.

.EXAMPLE
    .\Update-Watcher-Pinger.ps1 -SetToken

    Asks for the API key and keeps it, in an elevated PowerShell.

.EXAMPLE
    .\Update-Watcher-Pinger.ps1 -Folder 'D:\Shares\Technicians\Watcher Pinger' -WatcherNmsUrl https://nms.example.com

    Updates the shared folder of a file server.
#>
[CmdletBinding()]
param(
    # Where watcher-pinger.exe and watcher-pinger.json are kept, the folder of this script if not given: read by
    # whoever runs the pinger, written by the administrators only, as a folder in Program Files is.
    [string] $Folder,

    # Watcher NMS, https://nms.example.com; without it only the program is updated.
    [string] $WatcherNmsUrl,

    # The API key of the Watcher NMS user, watcher-nms.token beside this script if not given.
    [string] $TokenFile,

    # Asks for the API key, keeps it in the TokenFile and ends.
    [switch] $SetToken,

    # Takes a release candidate (1.1.0-RC1) too.
    [switch] $Prerelease,

    [string] $Repository = 'Mapiiik/Watcher-Pinger',

    # Appends what is done to this file too.
    [string] $LogFile
)

Set-StrictMode -Version 3
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
# Windows PowerShell 5.1 has no $PSScriptRoot in the defaults of the parameters yet
if (-not $Folder) {
    $Folder = $PSScriptRoot
}
if (-not $TokenFile) {
    $TokenFile = Join-Path $PSScriptRoot 'watcher-nms.token'
}
# Windows PowerShell 5.1 on older systems offers no TLS 1.2 by itself
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Write-Log([string] $Text) {
    $line = '{0:yyyy-MM-dd HH:mm:ss} {1}' -f (Get-Date), $Text
    Write-Host $line
    if ($LogFile) {
        Add-Content -LiteralPath $LogFile -Value $line -Encoding UTF8
    }
}

function Test-Elevated {
    $principal = New-Object Security.Principal.WindowsPrincipal ([Security.Principal.WindowsIdentity]::GetCurrent())
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Lets only SYSTEM, which the task runs as, and the administrators at the file.
function Protect-File([string] $Path) {
    $acl = Get-Acl -LiteralPath $Path
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) {
        [void] $acl.RemoveAccessRule($rule)
    }
    foreach ($sid in 'S-1-5-18', 'S-1-5-32-544') {
        $who = New-Object Security.Principal.SecurityIdentifier $sid
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule $who, 'FullControl', 'Allow'))
    }
    $acl.SetOwner((New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'))
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Set-WatcherNmsToken {
    if (-not (Test-Elevated)) {
        throw '-SetToken needs PowerShell run as administrator.'
    }
    $secure = Read-Host 'API key of the Watcher NMS user of the role watcher-pinger' -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        $token = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr).Trim()
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
    if (-not $token) {
        throw 'No key given, nothing kept.'
    }

    # made anew, so that nobody who made the file before keeps a right to it
    if (Test-Path -LiteralPath $TokenFile) {
        Remove-Item -LiteralPath $TokenFile -Force
    }
    New-Item -ItemType File -Path $TokenFile | Out-Null
    Protect-File $TokenFile
    [IO.File]::WriteAllText($TokenFile, $token)
    Write-Log "The key is kept in $TokenFile, for SYSTEM and the administrators only."
}

function Get-WatcherNmsToken {
    if (-not (Test-Path -LiteralPath $TokenFile)) {
        throw "No API key for Watcher NMS in $TokenFile, -SetToken keeps one there."
    }
    $token = [IO.File]::ReadAllText($TokenFile).Trim()
    if (-not $token) {
        throw "$TokenFile is empty, -SetToken keeps the API key there."
    }
    return $token
}

function Get-StatusCode($ErrorRecord) {
    $property = $ErrorRecord.Exception.PSObject.Properties['Response']
    if ($property -and $property.Value) {
        return [int] $property.Value.StatusCode
    }
    return 0
}

# Puts the new file in place of the old one at once, keeping the rights of the old one. A pinger reading the file just
# then holds it for a moment, so it is tried a few times.
function Move-FileInPlace([string] $New, [string] $Target, [int] $Tries = 1) {
    for ($try = 1; ; $try++) {
        try {
            if (Test-Path -LiteralPath $Target) {
                [IO.File]::Replace($New, $Target, [NullString]::Value)
            } else {
                [IO.File]::Move($New, $Target)
            }
            return
        } catch {
            if ($try -ge $Tries) {
                throw
            }
            Start-Sleep -Seconds 1
        }
    }
}

function Update-Program {
    $releases = Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$Repository/releases?per_page=30" `
        -Headers @{ Accept = 'application/vnd.github+json' }
    $release = $releases |
        Where-Object { -not $_.draft -and ($Prerelease -or -not $_.prerelease) } |
        Sort-Object published_at -Descending |
        Select-Object -First 1
    if (-not $release) {
        $hint = if ($Prerelease) { '' } else { ', -Prerelease takes a release candidate too' }
        throw "No release of $Repository yet$hint."
    }

    $program = $release.assets | Where-Object { $_.name -eq 'watcher-pinger.exe' }
    $sums = $release.assets | Where-Object { $_.name -eq 'SHA256SUMS' }
    if (-not $program -or -not $sums) {
        throw "Release $($release.tag_name) has no watcher-pinger.exe with SHA256SUMS."
    }
    $content = (Invoke-WebRequest -UseBasicParsing -Uri $sums.browser_download_url).Content
    if ($content -is [byte[]]) {
        $content = [Text.Encoding]::UTF8.GetString($content)
    }
    $expected = $null
    foreach ($line in $content -split "`n") {
        if ($line -match '^([0-9a-fA-F]{64}) [ *]watcher-pinger\.exe\s*$') {
            $expected = $Matches[1]
        }
    }
    if (-not $expected) {
        throw "SHA256SUMS of $($release.tag_name) has no line for watcher-pinger.exe."
    }

    # programs moved aside before, where no pinger runs them any more
    Get-ChildItem -LiteralPath $Folder -Filter 'watcher-pinger.exe.*.old' |
        Remove-Item -Force -ErrorAction SilentlyContinue

    $target = Join-Path $Folder 'watcher-pinger.exe'
    if ((Test-Path -LiteralPath $target) -and (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -eq $expected) {
        Write-Log "watcher-pinger.exe is $($release.tag_name) already."
        return
    }

    $new = "$target.download"
    Invoke-WebRequest -UseBasicParsing -Uri $program.browser_download_url -OutFile $new
    if ((Get-FileHash -LiteralPath $new -Algorithm SHA256).Hash -ne $expected) {
        Remove-Item -LiteralPath $new
        throw "watcher-pinger.exe of $($release.tag_name) does not match its SHA256SUMS, the one there stays."
    }

    # A pinger running holds its program, which cannot be written over then, but can be renamed: the old one is moved
    # aside, the new one put in its place, and the old one deleted where no pinger runs it, or at a later update.
    $aside = '{0}.{1:yyyyMMddHHmmss}.old' -f $target, (Get-Date)
    if (Test-Path -LiteralPath $target) {
        Move-Item -LiteralPath $target -Destination $aside
    }
    try {
        Move-Item -LiteralPath $new -Destination $target
    } catch {
        if (Test-Path -LiteralPath $aside) {
            Move-Item -LiteralPath $aside -Destination $target
        }
        throw
    }
    Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $aside) {
        Write-Log "watcher-pinger.exe is $($release.tag_name) now, for the pingers started from now on."
    } else {
        Write-Log "watcher-pinger.exe is $($release.tag_name) now."
    }
}

function Update-Network {
    $token = Get-WatcherNmsToken
    $target = Join-Path $Folder 'watcher-pinger.json'
    $new = "$target.download"
    $uri = $WatcherNmsUrl.TrimEnd('/') + '/api/access-points/pinger.json'

    try {
        # no redirect, so that the key goes nowhere else
        Invoke-WebRequest -UseBasicParsing -Uri $uri -MaximumRedirection 0 -OutFile $new `
            -Headers @{ Authorization = "Bearer $token"; Accept = 'application/json' }
    } catch {
        $problem = $_
        Remove-Item -LiteralPath $new -Force -ErrorAction SilentlyContinue
        $status = Get-StatusCode $problem
        if (($status -ge 300 -and $status -lt 400) -or $problem.FullyQualifiedErrorId -like 'MaximumRedirectExceeded*') {
            throw "Watcher NMS sent the request elsewhere, where the key is not sent: is $WatcherNmsUrl its address, with https?"
        }
        switch ($status) {
            401 { throw "Watcher NMS did not take the API key in $TokenFile (401)." }
            403 { throw 'Watcher NMS refused the network (403): the address of this computer is not in its PINGER_API_ALLOWED_ADDRESSES, or the user has not the role watcher-pinger.' }
            default { throw "Watcher NMS did not give the network: $($problem.Exception.Message)" }
        }
    }

    # an error page or a file cut short is not put in place
    $text = [IO.File]::ReadAllText($new)
    $head = $text.Substring(0, [Math]::Min(1024, $text.Length))
    if ($head -notmatch '^\s*\{\s*"format"\s*:\s*"watcher-pinger"' -or $text.TrimEnd() -notmatch '\}$') {
        Remove-Item -LiteralPath $new
        throw 'Watcher NMS answered with something else than the network, the file there stays.'
    }

    Move-FileInPlace $new $target 10
    Write-Log ('watcher-pinger.json is from now, {0:N0} kB.' -f ((Get-Item -LiteralPath $target).Length / 1KB))
}

if ($SetToken) {
    Set-WatcherNmsToken
    exit 0
}

if (-not (Test-Path -LiteralPath $Folder)) {
    New-Item -ItemType Directory -Path $Folder | Out-Null
}

$failed = $false
try {
    Update-Program
} catch {
    Write-Log "watcher-pinger.exe not updated: $($_.Exception.Message)"
    $failed = $true
}
if ($WatcherNmsUrl) {
    try {
        Update-Network
    } catch {
        Write-Log "watcher-pinger.json not updated: $($_.Exception.Message)"
        $failed = $true
    }
}
if ($failed) {
    exit 1
}
