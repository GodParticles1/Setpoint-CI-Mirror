[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$ServerArgs
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Root = (Resolve-Path (Split-Path -Parent $MyInvocation.MyCommand.Path)).Path
$ServerExe = Join-Path $Root 'setpoint-server.exe'
$DataDir = Join-Path $Root 'data'
$LogsDir = Join-Path $Root 'logs'
$AgentsDir = Join-Path $Root 'agents'
$PidPath = Join-Path $DataDir 'setpoint-server.pid'
$PidTempPath = "$PidPath.tmp"
$LockPath = Join-Path $DataDir 'setpoint-launcher.lock'
$StdoutPath = Join-Path $LogsDir 'setpoint-server.log'
$StderrPath = Join-Path $LogsDir 'setpoint-server-error.log'

function Remove-PidState {
    Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $PidTempPath -Force -ErrorAction SilentlyContinue
}

function Get-OwnedServerProcess {
    if (-not (Test-Path -LiteralPath $PidPath)) {
        return $null
    }

    $raw = (Get-Content -LiteralPath $PidPath -Raw).Trim()
    $processId = 0
    if (-not [int]::TryParse($raw, [ref]$processId) -or $processId -le 0) {
        Write-Host 'Removing stale Setpoint PID state (invalid PID).'
        Remove-PidState
        return $null
    }

    $process = Get-Process -Id $processId -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Write-Host "Removing stale Setpoint PID state (PID $processId is not running)."
        Remove-PidState
        return $null
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
        return $null
    }
    return $process
}

if (-not (Test-Path -LiteralPath $ServerExe -PathType Leaf)) {
    throw "Setpoint Server executable was not found: $ServerExe"
}

New-Item -ItemType Directory -Path $DataDir, $LogsDir, $AgentsDir -Force | Out-Null

$lock = $null
try {
    try {
        $lock = [IO.File]::Open($LockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    } catch {
        throw 'Another Setpoint start/stop operation is already in progress.'
    }

    $running = Get-OwnedServerProcess
    if ($null -ne $running) {
        Write-Host "Setpoint Server is already running (PID $($running.Id))."
        Write-Host "Logs: $LogsDir"
        return
    }

    if ([string]::IsNullOrWhiteSpace($env:SETPOINT_SERVER_MANAGEMENT_LISTEN)) {
        $env:SETPOINT_SERVER_MANAGEMENT_LISTEN = '127.0.0.1:8080'
    }
    if ([string]::IsNullOrWhiteSpace($env:SETPOINT_SERVER_AGENT_LISTEN)) {
        $env:SETPOINT_SERVER_AGENT_LISTEN = '0.0.0.0:8081'
    }
    if (-not (Test-Path Env:SETPOINT_SERVER_AGENT_ADVERTISE_URL)) {
        $env:SETPOINT_SERVER_AGENT_ADVERTISE_URL = ''
    }
    if ([string]::IsNullOrWhiteSpace($env:SETPOINT_SERVER_AGENT_ARTIFACTS_DIR)) {
        $env:SETPOINT_SERVER_AGENT_ARTIFACTS_DIR = $AgentsDir
    }
    if ([string]::IsNullOrWhiteSpace($env:SETPOINT_SERVER_DATABASE_PATH)) {
        $env:SETPOINT_SERVER_DATABASE_PATH = Join-Path $DataDir 'setpoint.db'
    }

    $startParameters = @{
        FilePath = $ServerExe
        WorkingDirectory = $Root
        RedirectStandardOutput = $StdoutPath
        RedirectStandardError = $StderrPath
        PassThru = $true
        WindowStyle = 'Hidden'
    }
    if ($null -ne $ServerArgs -and $ServerArgs.Count -gt 0) {
        $startParameters.ArgumentList = $ServerArgs
    }

    $process = Start-Process @startParameters
    Start-Sleep -Milliseconds 700
    $process.Refresh()
    if ($process.HasExited) {
        Remove-PidState
        Write-Error "Setpoint Server exited during startup (exit code $($process.ExitCode))."
        if (Test-Path -LiteralPath $StderrPath) {
            Get-Content -LiteralPath $StderrPath -Tail 20 | Write-Host
        }
        if (Test-Path -LiteralPath $StdoutPath) {
            Get-Content -LiteralPath $StdoutPath -Tail 20 | Write-Host
        }
        throw 'Setpoint Server startup failed. See logs above.'
    }

    try {
        Set-Content -LiteralPath $PidTempPath -Value $process.Id -Encoding ASCII -NoNewline
        Move-Item -LiteralPath $PidTempPath -Destination $PidPath -Force
    } catch {
        Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
        Remove-PidState
        throw
    }

    Write-Host "Setpoint Server started (PID $($process.Id))."
    Write-Host "Management Web: http://$($env:SETPOINT_SERVER_MANAGEMENT_LISTEN)"
    Write-Host "Agent listener: $($env:SETPOINT_SERVER_AGENT_LISTEN)"
    if ([string]::IsNullOrWhiteSpace($env:SETPOINT_SERVER_AGENT_ADVERTISE_URL)) {
        Write-Host 'Agent callback: automatic per-bootstrap route selection; target runtime probe remains required.'
    } else {
        Write-Host "Agent callback override: $($env:SETPOINT_SERVER_AGENT_ADVERTISE_URL)"
    }
    Write-Host "Logs: $LogsDir"
    Write-Host "Data: $DataDir"
} finally {
    if ($null -ne $lock) {
        $lock.Dispose()
    }
}
