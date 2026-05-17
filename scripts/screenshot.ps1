# scripts/screenshot.ps1 - capture the JoyreactorDownload app window to a PNG.
#
# Usage:
#   powershell -File scripts/screenshot.ps1                       # -> docs/screen.png
#   powershell -File scripts/screenshot.ps1 -Path foo.png         # custom path
#   powershell -File scripts/screenshot.ps1 -Full                 # primary screen only
#   powershell -File scripts/screenshot.ps1 -Title "Other Title"  # match a different window
#   powershell -File scripts/screenshot.ps1 -List                 # list matching windows and exit
#
# Uses PrintWindow with PW_RENDERFULLCONTENT (= 2): captures the window's
# actual rendered content (including WebView2/Chromium GPU compositing)
# regardless of whether it is visible, occluded, or minimized. Plain
# CopyFromScreen would grab whatever pixels happen to be at the window's
# coordinates, so an overlapping window would be captured instead of ours.

param(
    [string]$Path = "$PSScriptRoot\..\screenshots\screen.png",
    [switch]$Full,
    [switch]$List,
    [string]$Title = "Joyreactor Downloader"
)

. "$PSScriptRoot\_lib.ps1"
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

# Extend Win32App with the PrintWindow signature - only needed by this script.
if (-not ("Win32Snap" -as [type])) {
    Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Win32Snap {
    [DllImport("user32.dll")]
    public static extern bool PrintWindow(IntPtr hWnd, IntPtr hdcBlt, uint nFlags);
}
"@
}

$Path = [System.IO.Path]::GetFullPath($Path)

if ($Full) {
    $rect = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $bmp = New-Object System.Drawing.Bitmap $rect.Width, $rect.Height
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen($rect.Location, [System.Drawing.Point]::Empty, $rect.Size)
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $g.Dispose(); $bmp.Dispose()
    Write-Output "Saved primary screen: $Path ($((Get-Item $Path).Length) bytes)"
    return
}

if ($List) {
    # Walk all visible windows and dump those with the matching title.
    $hits = New-Object System.Collections.Generic.List[object]
    $cb = [Win32App+EnumWindowsProc]{
        param($hWnd, $lParam)
        if (-not [Win32App]::IsWindowVisible($hWnd)) { return $true }
        $len = [Win32App]::GetWindowTextLength($hWnd)
        if ($len -eq 0) { return $true }
        $sb = New-Object System.Text.StringBuilder ($len + 1)
        [void][Win32App]::GetWindowText($hWnd, $sb, $sb.Capacity)
        if ($sb.ToString() -eq $Title) {
            $hits.Add(@{ Handle = $hWnd; Title = $sb.ToString() })
        }
        return $true
    }
    [void][Win32App]::EnumWindows($cb, [IntPtr]::Zero)
    if ($hits.Count -eq 0) {
        Write-Output "No visible top-level window with title exactly '$Title'."
    } else {
        $hits | ForEach-Object { "{0:X}  {1}" -f $_.Handle.ToInt64(), $_.Title } | Write-Output
    }
    return
}

$hWnd = Require-AppWindow -Title $Title
Focus-AppWindow -Hwnd $hWnd -SettleMs 0   # PrintWindow doesn't need foreground, save the time
$rect = Get-AppWindowRect -Hwnd $hWnd
if ($rect.Width -le 0 -or $rect.Height -le 0) {
    Write-Error ("Invalid window size: {0}x{1}" -f $rect.Width, $rect.Height)
    exit 1
}

$bmp = New-Object System.Drawing.Bitmap $rect.Width, $rect.Height
$g = [System.Drawing.Graphics]::FromImage($bmp)
$hdc = $g.GetHdc()
# PW_RENDERFULLCONTENT = 2 - required for Chromium-rendered content on
# Win10 1809+; without it Wails apps print as a transparent rectangle.
$ok = [Win32Snap]::PrintWindow($hWnd, $hdc, 2)
$g.ReleaseHdc($hdc)
$g.Dispose()

if (-not $ok) {
    Write-Error "PrintWindow returned false for window '$Title'."
    exit 1
}

$bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()

$hex = '{0:X}' -f $hWnd.ToInt64()
$size = (Get-Item $Path).Length
Write-Output ("Saved: {0} ({1} bytes, {2}x{3}, hWnd={4})" -f $Path, $size, $rect.Width, $rect.Height, $hex)
