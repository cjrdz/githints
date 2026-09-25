<#
.SYNOPSIS
    Install githints on Windows.

.DESCRIPTION
    irm https://raw.githubusercontent.com/cjrdz/githints/main/install.ps1 | iex

    The install directory matters more here than for most tools. `githints init`
    records the binary's path in each repository's git hooks, so installing to a
    stable location means those hooks keep working. Hooks fall back to PATH if
    the recorded path disappears, but a stable path avoids relying on that.

.PARAMETER Version
    Tag to install. Defaults to the latest release.

.PARAMETER BinDir
    Where to put the binary. Defaults to %LOCALAPPDATA%\Programs\githints.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:GITHINTS_VERSION,
    [string]$BinDir = $env:GITHINTS_BIN_DIR
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo = 'cjrdz/githints'

function Get-TargetArch {
    # PROCESSOR_ARCHITECTURE reports the *process* architecture, so a 32-bit
    # PowerShell on 64-bit Windows would report x86. The OS value is what the
    # download needs.
    switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64'   { return 'amd64' }
        'Arm64' { return 'arm64' }
        default {
            throw "unsupported architecture: $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)"
        }
    }
}

function Get-LatestVersion {
    # Follow the /releases/latest redirect rather than calling the API: no JSON
    # parsing, and no unauthenticated rate limit to hit.
    $resp = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -MaximumRedirection 0 -ErrorAction SilentlyContinue
    $location = $null
    if ($resp -and $resp.Headers.Location) {
        $location = $resp.Headers.Location
    } else {
        # Newer PowerShell throws rather than returning the 302; follow it.
        $location = (Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -UseBasicParsing).BaseResponse.RequestMessage.RequestUri.AbsoluteUri
    }
    $tag = ($location -split '/')[-1]
    if (-not $tag -or $tag -eq 'latest') {
        throw 'could not determine the latest version'
    }
    return $tag
}

function Add-ToUserPath([string]$Dir) {
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $current) { $current = '' }
    $parts = $current -split ';' | Where-Object { $_ -ne '' }
    if ($parts -contains $Dir) { return $false }

    $updated = (@($parts) + $Dir) -join ';'
    [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
    # Also update this session, so `githints` works without reopening a shell.
    $env:Path = "$env:Path;$Dir"
    return $true
}

$arch = Get-TargetArch
if (-not $Version) { $Version = Get-LatestVersion }
$number = $Version.TrimStart('v')
if (-not $BinDir) { $BinDir = Join-Path $env:LOCALAPPDATA 'Programs\githints' }

$archive = "githints_${number}_windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/$Version"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("githints-" + [guid]::NewGuid().ToString('N'))

Write-Host "githints $Version (windows_$arch) -> $BinDir"

try {
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    $zipPath = Join-Path $tmp $archive

    # TLS 1.2 is not the default on Windows PowerShell 5.1, and GitHub requires it.
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri "$base/$archive" -OutFile $zipPath -UseBasicParsing

    # Verify against the published checksums. A tool that installs itself into
    # every repository's git hooks should not skip this.
    try {
        $sumsPath = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sumsPath -UseBasicParsing
        $line = Select-String -Path $sumsPath -Pattern ([regex]::Escape($archive)) | Select-Object -First 1
        if ($line) {
            $expected = ($line.Line -split '\s+')[0]
            $actual = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
            if ($expected.ToLower() -ne $actual) {
                throw "checksum mismatch for $archive"
            }
            Write-Host 'checksum ok'
        } else {
            Write-Warning "$archive not listed in checksums.txt; skipping verification"
        }
    } catch [System.Net.WebException] {
        Write-Warning 'checksums.txt unavailable; skipping verification'
    }

    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
    $extracted = Join-Path $tmp 'githints.exe'
    if (-not (Test-Path $extracted)) {
        throw 'githints.exe was not found in the archive'
    }

    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    $target = Join-Path $BinDir 'githints.exe'
    # Windows refuses to overwrite a running executable; renaming it aside
    # lets the install succeed and leaves the old file to be cleaned up later.
    if (Test-Path $target) {
        $old = "$target.old"
        Remove-Item -Path $old -Force -ErrorAction SilentlyContinue
        try { Move-Item -Path $target -Destination $old -Force } catch { }
    }
    Move-Item -Path $extracted -Destination $target -Force

    Write-Host "installed $target"

    if (Add-ToUserPath $BinDir) {
        Write-Host "added $BinDir to your user PATH"
    }

    & $target version | Out-Null
    Write-Host ''
    Write-Host "Next: run 'githints init' inside a repository you want tracked."
} finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
