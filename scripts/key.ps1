# scripts/key.ps1 - send a single keystroke (or a sequence) to the app.
#
# Usage:
#   powershell -File scripts/key.ps1 -Key Escape
#   powershell -File scripts/key.ps1 -Key Enter
#   powershell -File scripts/key.ps1 -Key Tab
#   powershell -File scripts/key.ps1 -Key F5
#   powershell -File scripts/key.ps1 -Key Down
#   powershell -File scripts/key.ps1 -Key "^a"   # raw SendKeys (Ctrl+A here)
#
# The named keys map to SendKeys tokens. Pass anything starting with ^, +,
# or % as a raw SendKeys string when you need a modifier combo.

param(
    [Parameter(Mandatory=$true)][string]$Key,
    [string]$Title = "Joyreactor Downloader"
)

. "$PSScriptRoot\_lib.ps1"
Add-Type -AssemblyName System.Windows.Forms

$keyMap = @{
    "Escape"    = "{ESC}"
    "Esc"       = "{ESC}"
    "Enter"     = "{ENTER}"
    "Return"    = "{ENTER}"
    "Tab"       = "{TAB}"
    "Space"     = " "
    "Backspace" = "{BACKSPACE}"
    "Delete"    = "{DELETE}"
    "Home"      = "{HOME}"
    "End"       = "{END}"
    "PageUp"    = "{PGUP}"
    "PageDown"  = "{PGDN}"
    "Up"        = "{UP}"
    "Down"      = "{DOWN}"
    "Left"      = "{LEFT}"
    "Right"     = "{RIGHT}"
    "F1"        = "{F1}"
    "F2"        = "{F2}"
    "F3"        = "{F3}"
    "F4"        = "{F4}"
    "F5"        = "{F5}"
    "F11"       = "{F11}"
    "F12"       = "{F12}"
}

$send = $keyMap[$Key]
if (-not $send) {
    # Passthrough - assume the caller knows SendKeys syntax (e.g. ^a, %{F4}).
    $send = $Key
}

$hWnd = Require-AppWindow -Title $Title
Focus-AppWindow -Hwnd $hWnd

[System.Windows.Forms.SendKeys]::SendWait($send)
Write-Output "Sent key: $Key (-> $send)"
