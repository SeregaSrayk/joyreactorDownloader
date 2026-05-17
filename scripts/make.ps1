# scripts/make.ps1 - Windows-friendly alternative to `make <target>` for
# users who don't want to install GNU Make. Implements the same targets
# defined in the project Makefile (build / dev / test / clean / etc.).
#
# Usage:
#   powershell -File scripts/make.ps1               # list targets
#   powershell -File scripts/make.ps1 build         # production build
#   powershell -File scripts/make.ps1 dev           # wails dev
#   powershell -File scripts/make.ps1 test          # go test ./...
#
# If you have make installed (e.g. via `choco install make` or
# `scoop install make`), `make build` is equivalent and shorter.

param([string]$Target = "help")

$root    = Split-Path -Parent $PSScriptRoot
$guiDir  = Join-Path $root "cmd/joyreactor-gui"
$guiBin  = Join-Path $guiDir "build/bin/joyreactorDownloader"
$exeName = "joyreactorDownloader.exe"

function Invoke-Target {
    param([string]$Name)
    switch ($Name) {
        "help" {
            Write-Output "Targets:"
            Write-Output "  build              Production build (wails build -clean)"
            Write-Output "  build-nsis         Build + NSIS installer (Windows only)"
            Write-Output "  dev                wails dev (hot-reload)"
            Write-Output "  run                Build and launch the exe"
            Write-Output "  cli                Compile the CLI to bin/joyreactor-dl"
            Write-Output "  test               go test ./..."
            Write-Output "  test-integration   go test -tags=integration ./..."
            Write-Output "  vet                go vet ./..."
            Write-Output "  fmt                go fmt ./..."
            Write-Output "  check              fmt + vet + test"
            Write-Output "  kill               taskkill the running exe"
            Write-Output "  clean              Remove build/bin and dist artifacts"
            Write-Output "  deps               go mod tidy + npm install"
            Write-Output "  screenshot         Capture app window -> screenshots/screen.png"
            Write-Output "  screenshot-list    List candidate windows"
        }
        "kill" {
            cmd /c "taskkill /F /IM $exeName 2>nul"
            return  # ignore exit code if not running
        }
        "build" {
            Invoke-Target -Name kill
            Push-Location $guiDir
            try { wails build -clean } finally { Pop-Location }
        }
        "build-nsis" {
            Invoke-Target -Name kill
            Push-Location $guiDir
            try { wails build -clean -nsis } finally { Pop-Location }
        }
        "dev" {
            Push-Location $guiDir
            try { wails dev } finally { Pop-Location }
        }
        "run" {
            Invoke-Target -Name build
            & "$guiBin.exe"
        }
        "cli" {
            New-Item -ItemType Directory -Force -Path bin | Out-Null
            go build -o bin/joyreactor-dl.exe ./cmd/joyreactor-dl
        }
        "test"             { go test ./... }
        "test-integration" { go test -tags=integration ./... }
        "vet"              { go vet ./... }
        "fmt"              { go fmt ./... }
        "check" {
            Invoke-Target -Name fmt
            Invoke-Target -Name vet
            Invoke-Target -Name test
        }
        "clean" {
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue `
                "$guiDir/build/bin", "$guiDir/frontend/dist"
            Get-ChildItem screenshots/*.png -ErrorAction SilentlyContinue | Remove-Item -Force
        }
        "deps" {
            go mod tidy
            Push-Location "$guiDir/frontend"
            try { npm install } finally { Pop-Location }
        }
        "screenshot"      { & "$PSScriptRoot/screenshot.ps1" }
        "screenshot-list" { & "$PSScriptRoot/screenshot.ps1" -List }
        default {
            Write-Error "Unknown target: $Name (run without args for help)"
            exit 1
        }
    }
}

Push-Location $root
try { Invoke-Target -Name $Target } finally { Pop-Location }
