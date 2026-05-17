# scripts/click.ps1 - mouse click at window-relative coordinates.
#
# Usage:
#   powershell -File scripts/click.ps1 -X 100 -Y 200            # LMB
#   powershell -File scripts/click.ps1 -X 100 -Y 200 -Right     # RMB
#   powershell -File scripts/click.ps1 -X 100 -Y 200 -Double    # double LMB
#
# Coords are RELATIVE to the top-left of the target window (default
# "Joyreactor Downloader"), so a moved window doesn't break automation.

param(
    [Parameter(Mandatory=$true)][int]$X,
    [Parameter(Mandatory=$true)][int]$Y,
    [switch]$Right,
    [switch]$Double,
    [string]$Title = "Joyreactor Downloader",
    [int]$DelayMs = 50
)

. "$PSScriptRoot\_lib.ps1"

$hWnd = Require-AppWindow -Title $Title
Focus-AppWindow -Hwnd $hWnd

$abs = Convert-ToScreenPoint -Hwnd $hWnd -X $X -Y $Y
[void][Win32App]::SetCursorPos($abs.X, $abs.Y)
Start-Sleep -Milliseconds $DelayMs

if ($Right) {
    $down = [Win32App]::MOUSEEVENTF_RIGHTDOWN
    $up   = [Win32App]::MOUSEEVENTF_RIGHTUP
    $label = "RMB"
} else {
    $down = [Win32App]::MOUSEEVENTF_LEFTDOWN
    $up   = [Win32App]::MOUSEEVENTF_LEFTUP
    $label = "LMB"
}

[Win32App]::mouse_event($down, 0, 0, 0, [UIntPtr]::Zero)
[Win32App]::mouse_event($up,   0, 0, 0, [UIntPtr]::Zero)

if ($Double) {
    Start-Sleep -Milliseconds 60
    [Win32App]::mouse_event($down, 0, 0, 0, [UIntPtr]::Zero)
    [Win32App]::mouse_event($up,   0, 0, 0, [UIntPtr]::Zero)
    $label = "$label x2"
}

Write-Output ("Clicked {0} at window({1},{2}) screen({3},{4})" -f $label, $X, $Y, $abs.X, $abs.Y)
