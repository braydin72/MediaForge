# build.ps1 — build mediaforge.exe and mediaforge-tray.exe with an injected build number.
#
# The build number is injected into internal/version.Build via -ldflags -X,
# matching the scheme used by the Dockerfile and CI. Locally we derive it from
# the git commit count (CI uses the GitHub Actions run number instead).
#
# Usage:
#   .\build.ps1            # build number = git commit count
#   .\build.ps1 -Build 42  # explicit build number

param(
    [string]$Build = (git rev-list --count HEAD)
)

$ErrorActionPreference = 'Stop'
$pkg = 'github.com/braydin72/mediaforge/internal/version.Build'
$ldflags = "-X $pkg=$Build"

New-Item -ItemType Directory -Force -Path dist | Out-Null

Write-Host "Building mediaforge.exe (build $Build)..."
go build -ldflags $ldflags -o dist/mediaforge.exe ./cmd/mediaforge

Write-Host "Building mediaforge-tray.exe (build $Build)..."
# -H windowsgui marks the tray as a GUI subsystem app so Windows does not
# allocate a console window when it launches (no black command window).
go build -ldflags "$ldflags -H windowsgui" -o dist/mediaforge-tray.exe ./cmd/tray

Write-Host "Done. Binaries in .\dist\ (build $Build)."
