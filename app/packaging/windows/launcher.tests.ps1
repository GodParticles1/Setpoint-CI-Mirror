Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FreeTcpPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

$SourceRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$BuiltServer = Join-Path $SourceRoot 'setpoint-server.exe'
if (-not (Test-Path -LiteralPath $BuiltServer -PathType Leaf)) {
    throw "Build setpoint-server.exe before running launcher tests: $BuiltServer"
}

$TestRoot = Join-Path ([IO.Path]::GetTempPath()) ("setpoint-launcher-test-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $TestRoot -Force | Out-Null
Copy-Item -LiteralPath $BuiltServer -Destination (Join-Path $TestRoot 'setpoint-server.exe')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'start.ps1') -Destination (Join-Path $TestRoot 'start.ps1')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'stop.ps1') -Destination (Join-Path $TestRoot 'stop.ps1')

$originalManagement = $env:SETPOINT_SERVER_MANAGEMENT_LISTEN
$originalAgent = $env:SETPOINT_SERVER_AGENT_LISTEN
$originalAdvertise = $env:SETPOINT_SERVER_AGENT_ADVERTISE_URL
$originalArtifacts = $env:SETPOINT_SERVER_AGENT_ARTIFACTS_DIR
$originalDatabase = $env:SETPOINT_SERVER_DATABASE_PATH

try {
    $managementPort = Get-FreeTcpPort
    $agentPort = Get-FreeTcpPort
    $env:SETPOINT_SERVER_MANAGEMENT_LISTEN = "127.0.0.1:$managementPort"
    $env:SETPOINT_SERVER_AGENT_LISTEN = "127.0.0.1:$agentPort"
    $env:SETPOINT_SERVER_AGENT_ADVERTISE_URL = ''
    Remove-Item Env:SETPOINT_SERVER_AGENT_ARTIFACTS_DIR -ErrorAction SilentlyContinue
    Remove-Item Env:SETPOINT_SERVER_DATABASE_PATH -ErrorAction SilentlyContinue

    & (Join-Path $TestRoot 'start.ps1')
    $pidPath = Join-Path $TestRoot 'data\setpoint-server.pid'
    if (-not (Test-Path -LiteralPath $pidPath)) {
        throw 'first start did not create PID state'
    }
    $firstProcessId = [int](Get-Content -LiteralPath $pidPath -Raw)
    if ($null -eq (Get-Process -Id $firstProcessId -ErrorAction SilentlyContinue)) {
        throw 'first start process is not running'
    }

    & (Join-Path $TestRoot 'start.ps1')
    $secondProcessId = [int](Get-Content -LiteralPath $pidPath -Raw)
    if ($secondProcessId -ne $firstProcessId) {
        throw "repeated start launched a second server: first=$firstProcessId second=$secondProcessId"
    }

    & (Join-Path $TestRoot 'stop.ps1')
    if (Test-Path -LiteralPath $pidPath) {
        throw 'stop left PID state behind'
    }
    if ($null -ne (Get-Process -Id $firstProcessId -ErrorAction SilentlyContinue)) {
        throw "stop left server PID $firstProcessId running"
    }

    New-Item -ItemType Directory -Path (Split-Path -Parent $pidPath) -Force | Out-Null
    Set-Content -LiteralPath $pidPath -Value '2147483647' -Encoding ASCII -NoNewline
    & (Join-Path $TestRoot 'start.ps1')
    $replacementProcessId = [int](Get-Content -LiteralPath $pidPath -Raw)
    if ($replacementProcessId -eq 2147483647 -or $null -eq (Get-Process -Id $replacementProcessId -ErrorAction SilentlyContinue)) {
        throw 'stale PID was not replaced by a running server'
    }
    & (Join-Path $TestRoot 'stop.ps1')

    Write-Host 'WINDOWS_LAUNCHER_TESTS=PASS'
    Write-Host 'TESTS=repeated-start,stale-pid,owned-process-stop'
} finally {
    try {
        $pidPath = Join-Path $TestRoot 'data\setpoint-server.pid'
        if (Test-Path -LiteralPath $pidPath) {
            & (Join-Path $TestRoot 'stop.ps1') | Out-Null
        }
    } catch {
        Write-Warning "launcher test cleanup failed: $($_.Exception.Message)"
    }
    Remove-Item -LiteralPath $TestRoot -Recurse -Force -ErrorAction SilentlyContinue

    if ($null -eq $originalManagement) { Remove-Item Env:SETPOINT_SERVER_MANAGEMENT_LISTEN -ErrorAction SilentlyContinue } else { $env:SETPOINT_SERVER_MANAGEMENT_LISTEN = $originalManagement }
    if ($null -eq $originalAgent) { Remove-Item Env:SETPOINT_SERVER_AGENT_LISTEN -ErrorAction SilentlyContinue } else { $env:SETPOINT_SERVER_AGENT_LISTEN = $originalAgent }
    if ($null -eq $originalAdvertise) { Remove-Item Env:SETPOINT_SERVER_AGENT_ADVERTISE_URL -ErrorAction SilentlyContinue } else { $env:SETPOINT_SERVER_AGENT_ADVERTISE_URL = $originalAdvertise }
    if ($null -eq $originalArtifacts) { Remove-Item Env:SETPOINT_SERVER_AGENT_ARTIFACTS_DIR -ErrorAction SilentlyContinue } else { $env:SETPOINT_SERVER_AGENT_ARTIFACTS_DIR = $originalArtifacts }
    if ($null -eq $originalDatabase) { Remove-Item Env:SETPOINT_SERVER_DATABASE_PATH -ErrorAction SilentlyContinue } else { $env:SETPOINT_SERVER_DATABASE_PATH = $originalDatabase }
}
