param([string]$WorkspaceRoot = (Split-Path -Parent $PSScriptRoot))
$ErrorActionPreference = 'Stop'
$taskRoot = [IO.Path]::GetFullPath($WorkspaceRoot)
$runtimeRoot = Join-Path $taskRoot '.keyrelay-runtime'
$configPath = Join-Path $runtimeRoot 'config.yaml'
$serverPath = Join-Path $runtimeRoot 'cli-proxy-api.exe'
$panelPath = Join-Path $taskRoot '.keyrelay-tools/panel-build'
if (-not (Test-Path -LiteralPath $configPath)) { throw 'Local website config is not prepared.' }
if (-not (Test-Path -LiteralPath (Join-Path $panelPath 'vite.local.config.ts'))) { throw 'Panel dev setup is missing.' }

$serverListener = Get-NetTCPConnection -State Listen -LocalPort 8319 -ErrorAction SilentlyContinue
if (-not $serverListener) {
    $server = Start-Process -FilePath $serverPath -ArgumentList @('--config', ('"{0}"' -f $configPath), '--local-model') -WorkingDirectory $runtimeRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtimeRoot 'logs/server-out.log') -RedirectStandardError (Join-Path $runtimeRoot 'logs/server-error.log') -PassThru
    Set-Content -LiteralPath (Join-Path $runtimeRoot 'server.pid') -Value $server.Id
} else {
    $owner = Get-Process -Id $serverListener[0].OwningProcess
    if ($owner.Path -ne $serverPath) { throw 'Port 8319 belongs to another process.' }
}

$panelListener = Get-NetTCPConnection -State Listen -LocalPort 5173 -ErrorAction SilentlyContinue
if (-not $panelListener) {
    $nodeExecutable = (Get-Command node.exe).Source
    $viteExecutable = Join-Path $panelPath 'node_modules/vite/bin/vite.js'
    $panel = Start-Process -FilePath $nodeExecutable -ArgumentList @(('"{0}"' -f $viteExecutable), '--config', 'vite.local.config.ts') -WorkingDirectory $panelPath -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtimeRoot 'logs/panel-out.log') -RedirectStandardError (Join-Path $runtimeRoot 'logs/panel-error.log') -PassThru
    Set-Content -LiteralPath (Join-Path $runtimeRoot 'panel.pid') -Value $panel.Id
} else {
    $expectedPid = Get-Content -LiteralPath (Join-Path $runtimeRoot 'panel.pid') -ErrorAction SilentlyContinue
    if ($panelListener[0].OwningProcess -ne $expectedPid) { throw 'Port 5173 belongs to another process.' }
}
Write-Output 'Administration processes started. Main panel: http://127.0.0.1:5173'
