# build.ps1 — build mediaforge.exe and mediaforge-tray.exe with an injected build number.
#
# The build number is injected into internal/version.Build via -ldflags -X,
# matching the scheme used by the Dockerfile and CI. Locally we derive it from
# the git commit count (CI uses the GitHub Actions run number instead).
#
# Version is derived from the nearest git tag (vX.Y.Z -> X.Y.Z) when available,
# so local builds report the same version CI would for that commit. Falls back
# to the hardcoded default in internal/version/version.go if no tag is found.
#
# Usage:
#   .\build.ps1            # build number = git commit count, version = nearest tag
#   .\build.ps1 -Build 42  # explicit build number

param(
    [string]$Build = (git rev-list --count HEAD),
    [string]$Version = ((git describe --tags --abbrev=0 2>$null) -replace '^v', '')
)

$ErrorActionPreference = 'Stop'
$buildPkg = 'github.com/braydin72/mediaforge/internal/version.Build'
$ldflags = "-X $buildPkg=$Build"
if ($Version) {
    $versionPkg = 'github.com/braydin72/mediaforge/internal/version.Version'
    $ldflags = "$ldflags -X $versionPkg=$Version"
}

New-Item -ItemType Directory -Force -Path dist | Out-Null

Write-Host "Building mediaforge.exe (build $Build)..."
go build -ldflags $ldflags -o dist/mediaforge.exe ./cmd/mediaforge

Write-Host "Building mediaforge-tray.exe (build $Build)..."
# -H windowsgui marks the tray as a GUI subsystem app so Windows does not
# allocate a console window when it launches (no black command window).
go build -ldflags "$ldflags -H windowsgui" -o dist/mediaforge-tray.exe ./cmd/tray

Write-Host "Done. Binaries in .\dist\ (build $Build)."
