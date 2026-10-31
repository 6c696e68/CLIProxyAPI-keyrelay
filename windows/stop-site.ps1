param([string]$WorkspaceRoot = (Split-Path -Parent $PSScriptRoot))
$ErrorActionPreference = 'Stop'
$taskRoot = [IO.Path]::GetFullPath($WorkspaceRoot)
$runtimeRoot = Join-Path $taskRoot '.keyrelay-runtime'
if (Test-Path -LiteralPath (Join-Path $runtimeRoot 'relay-install.json')) {
    & python (Join-Path $PSScriptRoot 'relay_manager.py') stop --workspace-root $taskRoot
    if ($LASTEXITCODE -ne 0) { throw 'Could not safely stop the model relay.' }
}
foreach ($kind in @('server','panel')) {
    $pidFile = Join-Path $runtimeRoot ($kind + '.pid')
    if (-not (Test-Path -LiteralPath $pidFile)) { continue }
    $rawPid = [string](Get-Content -LiteralPath $pidFile -Raw)
    $processId = 0
    if (-not [int]::TryParse($rawPid.Trim(), [ref]$processId)) {
        # An unparsable pid file is stale: remove it and never stop anything.
        Remove-Item -LiteralPath $pidFile -Force
        continue
    }
    $process = Get-CimInstance Win32_Process -Filter ('ProcessId = ' + $processId)
    if (-not $process) {
        Remove-Item -LiteralPath $pidFile -Force
        continue
    }
    if ($kind -eq 'server') {
        $expected = Join-Path $runtimeRoot 'cli-proxy-api.exe'
        if ($process.ExecutablePath -ne $expected) {
            # The saved PID belongs to another process: it is stale, never stop it.
            Remove-Item -LiteralPath $pidFile -Force
            continue
        }
    } else {
        $expected = Join-Path $taskRoot '.keyrelay-tools/panel-build'
        $commandLine = [string]$process.CommandLine
        if ($process.Name -ne 'node.exe' -or -not $commandLine.Contains($expected) -or -not $commandLine.Contains('vite.local.config.ts')) {
            # The saved PID belongs to another process: it is stale, never stop it.
            Remove-Item -LiteralPath $pidFile -Force
            continue
        }
    }
    Stop-Process -Id $processId
    Wait-Process -Id $processId -Timeout 10 -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $pidFile -Force
}
Write-Host 'The local site processes have stopped. Credentials and databases were preserved.'
