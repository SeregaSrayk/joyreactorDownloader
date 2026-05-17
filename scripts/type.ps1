# scripts/type.ps1 - type a string into the focused input.
#
# Usage:
#   powershell -File scripts/type.ps1 -Text "hello"
#   powershell -File scripts/type.ps1 -Text "тег" -ClearFirst   # Ctrl+A, Del, then type
#
# Uses SendKeys.SendWait which supports Unicode (Cyrillic OK). SendKeys'
# special chars (+ ^ % ~ ( ) { }) get auto-escaped so a literal "{tag}"
# typed verbatim doesn't become a modifier sequence. Pass -Raw if you
# actually want SendKeys syntax (modifier combos etc.).

param(
    [Parameter(Mandatory=$true)][string]$Text,
    [switch]$ClearFirst,
    [switch]$Raw,
    [string]$Title = "Joyreactor Downloader",
    [int]$DelayMs = 80
)

. "$PSScriptRoot\_lib.ps1"
Add-Type -AssemblyName System.Windows.Forms

$hWnd = Require-AppWindow -Title $Title
Focus-AppWindow -Hwnd $hWnd
Start-Sleep -Milliseconds $DelayMs

if ($ClearFirst) {
    [System.Windows.Forms.SendKeys]::SendWait("^a")
    Start-Sleep -Milliseconds 30
    [System.Windows.Forms.SendKeys]::SendWait("{DELETE}")
    Start-Sleep -Milliseconds 30
}

if ($Raw) {
    [System.Windows.Forms.SendKeys]::SendWait($Text)
} else {
    # Escape SendKeys' meta-characters so the input is treated as literal text.
    $escaped = $Text -replace '([+^%~(){}])', '{$1}'
    [System.Windows.Forms.SendKeys]::SendWait($escaped)
}
Write-Output "Typed: $Text"
