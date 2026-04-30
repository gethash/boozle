# boozle install script for Windows (PowerShell 5+ / pwsh).
#
# Usage:
#   iwr -useb https://github.com/gethash/boozle/releases/latest/download/install.ps1 | iex
#
# Env overrides:
#   $env:BOOZLE_VERSION       release tag (default: latest)
#   $env:BOOZLE_INSTALL_DIR   target dir (default: $env:LOCALAPPDATA\boozle)

$ErrorActionPreference = 'Stop'

$Repo = 'gethash/boozle'
$Version = if ($env:BOOZLE_VERSION) { $env:BOOZLE_VERSION } else { 'latest' }

if (-not [Environment]::Is64BitOperatingSystem) {
    throw "boozle requires a 64-bit Windows system."
}

if ($Version -eq 'latest') {
    $latest = Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$Repo/releases/latest"
    $Version = $latest.tag_name
}

$Target = 'windows-amd64'
$Archive = "boozle_${Version}_${Target}.zip"
$BaseUrl = "https://github.com/$Repo/releases/download/$Version"

$Tmp = New-Item -ItemType Directory -Force -Path (Join-Path $env:TEMP "boozle-install-$([guid]::NewGuid().Guid)")
try {
    Write-Host "boozle: downloading $Archive ($Version)..."
    $ZipPath = Join-Path $Tmp $Archive
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$Archive" -OutFile $ZipPath

    Write-Host "boozle: verifying SHA256..."
    $ChecksumsPath = Join-Path $Tmp 'checksums.txt'
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/checksums.txt" -OutFile $ChecksumsPath
    $expectedLine = Get-Content $ChecksumsPath | Where-Object { $_ -match "[\s]+$([regex]::Escape($Archive))$" }
    if (-not $expectedLine) { throw "$Archive not found in checksums.txt" }
    $expected = ($expectedLine -split '\s+')[0].ToLower()
    $actual = (Get-FileHash -Algorithm SHA256 $ZipPath).Hash.ToLower()
    if ($expected -ne $actual) {
        throw "checksum mismatch (expected $expected, got $actual)"
    }

    Write-Host "boozle: extracting..."
    Expand-Archive -LiteralPath $ZipPath -DestinationPath $Tmp -Force
    $SrcExe = Join-Path $Tmp "boozle_${Version}_${Target}\boozle.exe"
    if (-not (Test-Path $SrcExe)) { throw "boozle.exe missing inside archive" }

    $InstallDir = if ($env:BOOZLE_INSTALL_DIR) { $env:BOOZLE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'boozle' }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $Dest = Join-Path $InstallDir 'boozle.exe'
    Copy-Item -Force -Path $SrcExe -Destination $Dest

    Write-Host "boozle: installed $Version to $Dest"

    # Add to user PATH if not already there.
    $UserPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
    $segments = if ($UserPath) { $UserPath -split ';' } else { @() }
    if ($segments -notcontains $InstallDir) {
        Write-Host "boozle: adding $InstallDir to user PATH..."
        $newPath = if ($UserPath) { "$UserPath;$InstallDir" } else { $InstallDir }
        [Environment]::SetEnvironmentVariable('PATH', $newPath, 'User')
        Write-Host "boozle: open a new terminal for PATH changes to take effect"
    }
} finally {
    Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}
