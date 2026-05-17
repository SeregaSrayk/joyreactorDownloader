# scripts/scroll.ps1 - mouse wheel scroll at a window-relative point.
#
# Usage:
#   powershell -File scripts/scroll.ps1 -X 400 -Y 400 -Delta -120   # one notch down
#   powershell -File scripts/scroll.ps1 -X 400 -Y 400 -Delta 360    # 3 notches up
#
# Delta is in WHEEL_DELTA units (120 per notch). Positive = scroll up,
# negative = scroll down. Mouse is moved to (X,Y) so the wheel event lands
# on the intended scroll target (settings modal, results grid, etc.).

param(
    [Parameter(Mandatory=$true)][int]$X,
    [Parameter(Mandatory=$true)][int]$Y,
    [Parameter(Mandatory=$true)][int]$Delta,
    [string]$Title = "Joyreactor Downloader"
)

. "$PSScriptRoot\_lib.ps1"

$hWnd = Require-AppWindow -Title $Title
Focus-AppWindow -Hwnd $hWnd

$abs = Convert-ToScreenPoint -Hwnd $hWnd -X $X -Y $Y
[void][Win32App]::SetCursorPos($abs.X, $abs.Y)
Start-Sleep -Milliseconds 50

# mouse_event's cButtons param is reinterpreted as the wheel delta when
# dwFlags = MOUSEEVENTF_WHEEL. Positive = away from user (up).
[Win32App]::mouse_event([Win32App]::MOUSEEVENTF_WHEEL, 0, 0, $Delta, [UIntPtr]::Zero)

Write-Output ("Wheel delta {0} at window({1},{2})" -f $Delta, $X, $Y)
