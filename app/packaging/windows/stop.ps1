[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Root = (Resolve-Path (Split-Path -Parent $MyInvocation.MyCommand.Path)).Path
$ServerExe = Join-Path $Root 'setpoint-server.exe'
$DataDir = Join-Path $Root 'data'
$PidPath = Join-Path $DataDir 'setpoint-server.pid'
$PidTempPath = "$PidPath.tmp"
$LockPath = Join-Path $DataDir 'setpoint-launcher.lock'

function Remove-PidState {
    Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $PidTempPath -Force -ErrorAction SilentlyContinue
}

New-Item -ItemType Directory -Path $DataDir -Force | Out-Null

$lock = $null
try {
    try {
        $lock = [IO.File]::Open($LockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    } catch {
        throw 'Another Setpoint start/stop operation is already in progress.'
    }

    if (-not (Test-Path -LiteralPath $PidPath)) {
        Write-Host 'Setpoint Server is not running (no PID state).'
        return
    }

    $raw = (Get-Content -LiteralPath $PidPath -Raw).Trim()
    $processId = 0
    if (-not [int]::TryParse($raw, [ref]$processId) -or $processId -le 0) {
        Write-Host 'Removing stale Setpoint PID state (invalid PID).'
        Remove-PidState
        return
    }

    $process = Get-Process -Id $processId -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Write-Host "Removing stale Setpoint PID state (PID $processId is not running)."
        Remove-PidState
        return
    }

    $actualPath = $null
    try {
        $actualPath = $process.Path
    } catch {
        $actualPath = $null
    }
    if ([string]::IsNullOrWhiteSpace($actualPath) -or
        -not [string]::Equals([IO.Path]::GetFullPath($actualPath), [IO.Path]::GetFullPath($ServerExe), [StringComparison]::OrdinalIgnoreCase)) {
        Write-Warning "PID $processId does not belong to this Setpoint package. Removing stale PID state without stopping that process."
        Remove-PidState
        return
    }

    Stop-Process -Id $processId -ErrorAction Stop
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        if ($null -eq (Get-Process -Id $processId -ErrorAction SilentlyContinue)) {
            break
        }
        Start-Sleep -Milliseconds 100
    }
    if ($null -ne (Get-Process -Id $processId -ErrorAction SilentlyContinue)) {
        throw "Setpoint Server PID $processId did not stop."
    }
    Remove-PidState
    Write-Host "Setpoint Server stopped (PID $processId)."
} finally {
    if ($null -ne $lock) {
        $lock.Dispose()
    }
}
