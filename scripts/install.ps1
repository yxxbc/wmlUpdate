# Install the latest (or requested) wml-update release for Windows.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File install.ps1
#   $env:VERSION="v1.0.0"; $env:INSTALL_DIR="$env:LOCALAPPDATA\Programs\wml-update"; .\install.ps1
$ErrorActionPreference = "Stop"

$Repo = "yxxbc/wmlUpdate"
$Version = $env:VERSION
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\wml-update" }

if (-not $Version) {
    $release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    $Version = $release.tag_name
    if (-not $Version) { throw "could not determine latest release" }
}
$Bare = $Version.TrimStart("v")
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$Asset = "wml-update_${Bare}_windows_${Arch}.zip"
$Base = "https://github.com/$Repo/releases/download/$Version"
$Temp = Join-Path ([System.IO.Path]::GetTempPath()) ("wml-update-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $Temp | Out-Null

try {
    Write-Host "Downloading $Asset"
    $AssetPath = Join-Path $Temp $Asset
    Invoke-WebRequest "$Base/$Asset" -OutFile $AssetPath
    $ChecksumsPath = Join-Path $Temp "checksums.txt"
    Invoke-WebRequest "$Base/checksums.txt" -OutFile $ChecksumsPath

    $line = Get-Content $ChecksumsPath | Where-Object { $_ -match "\s+$([regex]::Escape($Asset))$" } | Select-Object -First 1
    if (-not $line) { throw "$Asset is not listed in checksums.txt" }
    $Expected = ($line -split '\s+')[0].ToLowerInvariant()
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $AssetPath).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) { throw "checksum mismatch" }

    Expand-Archive -Path $AssetPath -DestinationPath $Temp
    $Binary = Join-Path $Temp "wml-update.exe"
    if (-not (Test-Path $Binary)) { throw "archive does not contain wml-update.exe" }
    & $Binary version | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "downloaded binary failed to run" }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $Dest = Join-Path $InstallDir "wml-update.exe"
    Copy-Item $Binary $Dest -Force
    $LicenseDir = Join-Path $InstallDir "licenses"
    New-Item -ItemType Directory -Force -Path $LicenseDir | Out-Null
    foreach ($file in @("LICENSE", "NOTICE")) {
        $src = Join-Path $Temp $file
        if (Test-Path $src) { Copy-Item $src (Join-Path $LicenseDir $file) -Force }
    }
    Write-Host "Installed $Dest ($Version)"
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (($userPath -split ';') -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
        Write-Host "Added $InstallDir to the user PATH. Open a new terminal."
    }
}
finally {
    Remove-Item -Recurse -Force $Temp -ErrorAction SilentlyContinue
}
