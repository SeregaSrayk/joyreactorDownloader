# scripts/drag.ps1 - LMB drag from (X1,Y1) to (X2,Y2), all window-relative.
#
# Usage:
#   powershell -File scripts/drag.ps1 -X1 100 -Y1 200 -X2 400 -Y2 500
#
# Used for testing the rubber-band marquee selection. Moves in N small
# steps so the app sees mousemove events along the path - some apps (ours
# included) only update on mousemove, not just the final position.

param(
    [Parameter(Mandatory=$true)][int]$X1,
    [Parameter(Mandatory=$true)][int]$Y1,
    [Parameter(Mandatory=$true)][int]$X2,
    [Parameter(Mandatory=$true)][int]$Y2,
    [int]$Steps = 20,
    [int]$StepDelayMs = 15,
    [string]$Title = "Joyreactor Downloader"
)

. "$PSScriptRoot\_lib.ps1"

$hWnd = Require-AppWindow -Title $Title
Focus-AppWindow -Hwnd $hWnd

$startAbs = Convert-ToScreenPoint -Hwnd $hWnd -X $X1 -Y $Y1
$endAbs   = Convert-ToScreenPoint -Hwnd $hWnd -X $X2 -Y $Y2

[void][Win32App]::SetCursorPos($startAbs.X, $startAbs.Y)
Start-Sleep -Milliseconds 60

[Win32App]::mouse_event([Win32App]::MOUSEEVENTF_LEFTDOWN, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 30

for ($i = 1; $i -le $Steps; $i++) {
    $t = $i / [double]$Steps
    $x = [int]($startAbs.X + ($endAbs.X - $startAbs.X) * $t)
    $y = [int]($startAbs.Y + ($endAbs.Y - $startAbs.Y) * $t)
    [void][Win32App]::SetCursorPos($x, $y)
    Start-Sleep -Milliseconds $StepDelayMs
}

[Win32App]::mouse_event([Win32App]::MOUSEEVENTF_LEFTUP, 0, 0, 0, [UIntPtr]::Zero)
Write-Output ("Dragged window({0},{1}) -> ({2},{3})" -f $X1, $Y1, $X2, $Y2)
