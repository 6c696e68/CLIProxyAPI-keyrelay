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
    $processId = [int](Get-Content -LiteralPath $pidFile -Raw).Trim()
    $process = Get-CimInstance Win32_Process -Filter ('ProcessId = ' + $processId)
    if (-not $process) { continue }
    if ($kind -eq 'server') {
        $expected = Join-Path $runtimeRoot 'cli-proxy-api.exe'
        if ($process.ExecutablePath -ne $expected) { throw 'Saved server PID belongs to another process.' }
    } else {
        $expected = Join-Path $taskRoot '.keyrelay-tools/panel-build'
        if ($process.Name -ne 'node.exe' -or -not $process.CommandLine.Contains($expected) -or -not $process.CommandLine.Contains('vite.local.config.ts')) {
            throw 'Saved panel PID belongs to another process.'
        }
    }
    Stop-Process -Id $processId
    Wait-Process -Id $processId -Timeout 10 -ErrorAction SilentlyContinue
}
Write-Host 'The local site processes have stopped. Credentials and databases were preserved.'
