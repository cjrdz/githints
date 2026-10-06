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

.PARAMETER InsecureSkipVerify
    Install even if the checksum cannot be checked. Not recommended; a mismatch
    is always fatal. Also settable as GITHINTS_INSECURE_SKIP_VERIFY=1.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:GITHINTS_VERSION,
    [string]$BinDir = $env:GITHINTS_BIN_DIR,
    [switch]$InsecureSkipVerify = ($env:GITHINTS_INSECURE_SKIP_VERIFY -eq '1')
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
    # Follow the /releases/latest redirect and read where it lands (.../tag/vX.Y.Z)
    # rather than calling the API: no JSON parsing, and no unauthenticated rate
    # limit to hit. Reading the 302 itself is not reliable: PowerShell 7 throws on
    # an unfollowed redirect, even with -SkipHttpErrorCheck.
    $resp = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -UseBasicParsing
    $base = $resp.BaseResponse
    if ($base.PSObject.Properties['RequestMessage']) {
        $location = $base.RequestMessage.RequestUri.AbsoluteUri   # PowerShell 7
    } else {
        $location = $base.ResponseUri.AbsoluteUri                 # Windows PowerShell 5.1
    }
    $tag = ($location -split '/')[-1]
    if (-not $tag -or $tag -eq 'latest') {
        throw 'could not determine the latest version'
    }
    return $tag
}

# Invoke-Native runs a native command for its exit code only. Windows PowerShell
# 5.1 turns any stderr line from a native program into a terminating error when
# $ErrorActionPreference is 'Stop', so a signed-out gh printing a notice would
# otherwise end the install.
function Invoke-Native([scriptblock]$Command) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & $Command *> $null
        return $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $prev
    }
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
    # A tool that installs itself into every repository's git hooks should not
    # skip verification, so being unable to check is fatal unless the user
    # explicitly opts out. A mismatch is fatal regardless.
    function Fail-Unverified([string]$why) {
        if ($InsecureSkipVerify) {
            Write-Warning "$why; installing unverified because InsecureSkipVerify was set"
        } else {
            throw "$why; refusing to install unverified (set GITHINTS_INSECURE_SKIP_VERIFY=1 to override)"
        }
    }
    $sumsPath = Join-Path $tmp 'checksums.txt'
    $haveSums = $true
    try {
        Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sumsPath -UseBasicParsing
    } catch {
        $haveSums = $false
        Fail-Unverified 'could not download checksums.txt'
    }
    if ($haveSums) {
        $line = Select-String -Path $sumsPath -Pattern ("\s" + [regex]::Escape($archive) + '$') | Select-Object -First 1
        if ($line) {
            $expected = ($line.Line -split '\s+')[0]
            $actual = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
            if ($expected.ToLower() -ne $actual) {
                throw "checksum mismatch for $archive"
            }
            Write-Host 'checksum ok'
        } else {
            Fail-Unverified "$archive is not listed in checksums.txt"
        }
    }

    # The checksum proves integrity, not origin. When the GitHub CLI is
    # installed and signed in, also check the build-provenance attestation.
    # Only with a gh new enough to have `gh attestation` (2.49+) and signed in;
    # an older or signed-out gh is skipped, not treated as a failure.
    if (Get-Command gh -ErrorAction SilentlyContinue) {
        $hasAttestation = (Invoke-Native { gh attestation --help }) -eq 0
        $signedIn = (Invoke-Native { gh auth status }) -eq 0
        if ($hasAttestation -and $signedIn) {
            if ((Invoke-Native { gh attestation verify $zipPath --repo cjrdz/githints }) -eq 0) {
                Write-Host 'attestation ok'
            } else {
                Fail-Unverified "build provenance attestation did not verify for $archive"
            }
        }
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
        # Windows hands a PATH change only to programs started afterwards, and
        # Windows Terminal passes its own launch-time environment to every new
        # tab. An agent started from an old window cannot find `githints serve`,
        # and its MCP connection closes at once.
        Write-Host ''
        Write-Host 'Close and reopen your terminal app (all windows, not just the tab) and any'
        Write-Host 'open editor or agent (VS Code, Kiro, Claude Desktop, ...) so they see the new'
        Write-Host 'PATH; otherwise their MCP connection to githints fails to start.'
    }

    & $target version | Out-Null
    Write-Host ''
    Write-Host "Next: run 'githints setup' inside a repository you want tracked."
} finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
