# scripts/_lib.ps1 - shared P/Invoke + window helpers for the automation
# scripts. Dot-source from each script: `. "$PSScriptRoot\_lib.ps1"`.
#
# ASCII-only by design - Windows PowerShell 5.1 reads .ps1 in the system
# codepage unless a UTF-8 BOM is present, so non-ASCII chars in this file
# would corrupt on cp1251/cp1252 hosts.

if (-not ("Win32App" -as [type])) {
    Add-Type @"
using System;
using System.Runtime.InteropServices;
using System.Text;

public class Win32App {
    [DllImport("user32.dll")]
    public static extern bool EnumWindows(EnumWindowsProc enumProc, IntPtr lParam);
    public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

    [DllImport("user32.dll", CharSet=CharSet.Auto)]
    public static extern int GetWindowTextLength(IntPtr hWnd);

    [DllImport("user32.dll", CharSet=CharSet.Auto)]
    public static extern int GetWindowText(IntPtr hWnd, StringBuilder lpString, int nMaxCount);

    [DllImport("user32.dll")]
    public static extern bool IsWindowVisible(IntPtr hWnd);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static extern bool GetWindowRect(IntPtr hWnd, out RECT lpRect);

    [DllImport("user32.dll")]
    public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);

    [DllImport("user32.dll")]
    public static extern bool SetForegroundWindow(IntPtr hWnd);

    [DllImport("user32.dll")]
    public static extern bool BringWindowToTop(IntPtr hWnd);

    [DllImport("user32.dll", SetLastError=true)]
    public static extern bool SetCursorPos(int x, int y);

    [DllImport("user32.dll", SetLastError=true)]
    public static extern void mouse_event(uint dwFlags, int dx, int dy, int cButtons, UIntPtr dwExtraInfo);

    public const uint MOUSEEVENTF_LEFTDOWN  = 0x0002;
    public const uint MOUSEEVENTF_LEFTUP    = 0x0004;
    public const uint MOUSEEVENTF_RIGHTDOWN = 0x0008;
    public const uint MOUSEEVENTF_RIGHTUP   = 0x0010;
    public const uint MOUSEEVENTF_WHEEL     = 0x0800;
    public const uint MOUSEEVENTF_MOVE      = 0x0001;
    public const uint MOUSEEVENTF_ABSOLUTE  = 0x8000;

    public struct RECT { public int Left, Top, Right, Bottom; }
}
"@
}

# Find the first top-level visible window whose title equals $Title exactly.
# Returns [IntPtr]::Zero when nothing matches.
#
# The result hops through a generic List because PowerShell scriptblock
# scoping makes `$result = ...` inside the callback bind a callback-local
# variable, not the function-local one. A mutable container sidesteps
# that mess.
function Find-AppWindow {
    param([string]$Title = "Joyreactor Downloader")
    $bag = New-Object System.Collections.Generic.List[IntPtr]
    $cb = [Win32App+EnumWindowsProc]{
        param($hWnd, $lParam)
        if (-not [Win32App]::IsWindowVisible($hWnd)) { return $true }
        $len = [Win32App]::GetWindowTextLength($hWnd)
        if ($len -eq 0) { return $true }
        $sb = New-Object System.Text.StringBuilder ($len + 1)
        [void][Win32App]::GetWindowText($hWnd, $sb, $sb.Capacity)
        if ($sb.ToString() -eq $Title) { $bag.Add($hWnd); return $false }
        return $true
    }.GetNewClosure()
    [void][Win32App]::EnumWindows($cb, [IntPtr]::Zero)
    if ($bag.Count -gt 0) { return $bag[0] }
    return [IntPtr]::Zero
}

# Find a window or die with a clear error - cuts boilerplate at every call site.
function Require-AppWindow {
    param([string]$Title = "Joyreactor Downloader")
    $h = Find-AppWindow -Title $Title
    if ($h -eq [IntPtr]::Zero) {
        Write-Error "Window '$Title' not found. Is the app running?"
        exit 1
    }
    return $h
}

# Unminimize + bring to foreground. Returns true on apparent success.
# Windows can refuse SetForegroundWindow (focus-steal protection) but
# BringWindowToTop usually still works for our z-order purposes.
function Focus-AppWindow {
    param([IntPtr]$Hwnd, [int]$SettleMs = 80)
    [void][Win32App]::ShowWindow($Hwnd, 9)          # SW_RESTORE
    [void][Win32App]::SetForegroundWindow($Hwnd)
    [void][Win32App]::BringWindowToTop($Hwnd)
    Start-Sleep -Milliseconds $SettleMs
}

# Get window bounds as a hashtable: X, Y, Width, Height.
function Get-AppWindowRect {
    param([IntPtr]$Hwnd)
    $r = New-Object Win32App+RECT
    [void][Win32App]::GetWindowRect($Hwnd, [ref]$r)
    return @{
        X      = $r.Left
        Y      = $r.Top
        Width  = $r.Right - $r.Left
        Height = $r.Bottom - $r.Top
    }
}

# Convert window-relative coords to absolute screen coords. The automation
# scripts always speak in window-relative coords so a moved window doesn't
# break callers.
function Convert-ToScreenPoint {
    param([IntPtr]$Hwnd, [int]$X, [int]$Y)
    $rect = Get-AppWindowRect -Hwnd $Hwnd
    return @{ X = $rect.X + $X; Y = $rect.Y + $Y }
}
