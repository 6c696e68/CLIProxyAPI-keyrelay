param(
    [string]$WorkspaceRoot = (Split-Path -Parent $PSScriptRoot),
    [switch]$BuildOnly
)
$ErrorActionPreference = 'Stop'
function Invoke-CheckedNative {
    param([string]$FilePath, [string[]]$Arguments)
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        & $FilePath @Arguments 2>&1 | ForEach-Object { $_.ToString() }
        $nativeExit = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previousPreference }
    if ($nativeExit -ne 0) { throw ("Native command failed with exit " + $nativeExit + ': ' + $FilePath) }
}
$repoRoot = Split-Path -Parent $PSScriptRoot
$taskRoot = [IO.Path]::GetFullPath($WorkspaceRoot)
$toolRoot = Join-Path $taskRoot '.keyrelay-tools'
$runtimeRoot = Join-Path $taskRoot '.keyrelay-runtime'
if (-not $BuildOnly -and (Get-NetTCPConnection -State Listen -LocalPort 8319 -ErrorAction SilentlyContinue)) {
    throw 'Port 8319 is already running. Use START-SITE.cmd for the existing installation; setup will not overwrite it.'
}
$python = (Get-Command python.exe -ErrorAction Stop).Source
$null = Get-Command node.exe -ErrorAction Stop
$npm = (Get-Command npm.cmd -ErrorAction Stop).Source
$bun = (Get-Command bun -ErrorAction Stop).Source
$git = (Get-Command git.exe -ErrorAction Stop).Source
$go = Join-Path $toolRoot 'go/bin/go.exe'
if (-not (Test-Path -LiteralPath $go)) { $go = (Get-Command go.exe -ErrorAction Stop).Source }
New-Item -ItemType Directory -Path $toolRoot -Force | Out-Null
$env:PYTHONUTF8 = '1'
$env:GOCACHE = Join-Path $toolRoot 'build-cache'
$env:GOMODCACHE = Join-Path $toolRoot 'modules'
$env:GOPATH = Join-Path $toolRoot 'gopath'
$env:BUN_INSTALL_CACHE_DIR = Join-Path $toolRoot 'bun-cache'
$env:npm_config_cache = Join-Path $toolRoot 'npm-cache'
$env:ZIG_GLOBAL_CACHE_DIR = Join-Path $toolRoot 'zig-cache'
$env:ZIG_LOCAL_CACHE_DIR = Join-Path $toolRoot 'zig-local-cache'
$zig = Join-Path $toolRoot 'zig-x86_64-windows-0.15.2/zig.exe'
if (-not (Test-Path -LiteralPath $zig)) {
    $archive = Join-Path $toolRoot 'zig-x86_64-windows-0.15.2.zip'
    if (-not (Test-Path -LiteralPath $archive)) {
        Invoke-WebRequest -UseBasicParsing -Uri 'https://ziglang.org/download/0.15.2/zig-x86_64-windows-0.15.2.zip' -OutFile $archive
    }
    $digest = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
    if ($digest -ne '3a0ed1e8799a2f8ce2a6e6290a9ff22e6906f8227865911fb7ddedc3cc14cb0c') { throw 'Zig archive checksum mismatch.' }
    Invoke-CheckedNative $python @('-c', 'import sys,zipfile; zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])', $archive, $toolRoot)
}
$env:CGO_ENABLED = '1'
$env:CC = ('"{0}" cc -target x86_64-windows-gnu' -f $zig)
$pluginOut = Join-Path $toolRoot 'keyrelay-build/plugins/windows/amd64'
New-Item -ItemType Directory -Path $pluginOut -Force | Out-Null

Push-Location (Join-Path $repoRoot 'examples/plugin/key-shop/ui/admin')
try {
    Invoke-CheckedNative $npm @('ci')
    Invoke-CheckedNative $bun @('test', 'tests/live.test.ts')
    Invoke-CheckedNative $npm @('run', 'build')
} finally { Pop-Location }
$adminDist = Join-Path $repoRoot 'examples/plugin/key-shop/ui/admin/dist/index.html'
$adminEmbed = Join-Path $repoRoot 'examples/plugin/key-shop/go/web/admin.html'
Invoke-CheckedNative $python @('-c', "import pathlib,sys; pathlib.Path(sys.argv[2]).write_bytes(pathlib.Path(sys.argv[1]).read_bytes().replace(b'\r\n', b'\n'))", $adminDist, $adminEmbed)
foreach ($plugin in @('key-shop','ch-relay-vippro')) {
    Invoke-CheckedNative $go @('build', '-C', (Join-Path $repoRoot ('examples/plugin/' + $plugin + '/go')), '-buildmode=c-shared', '-o', (Join-Path $pluginOut ($plugin + '.dll')), '.')
    $header = Join-Path $pluginOut ($plugin + '.h')
    if (Test-Path -LiteralPath $header) { Remove-Item -LiteralPath $header }
}
$serverBuild = Join-Path $toolRoot 'keyrelay-build/cli-proxy-api.exe'
Invoke-CheckedNative $go @('build', '-C', $repoRoot, '-o', $serverBuild, './cmd/server')

$panelPath = Join-Path $toolRoot 'panel-build'
if (-not (Test-Path -LiteralPath (Join-Path $panelPath 'package.json'))) {
    Invoke-CheckedNative $git @('clone', '--filter=blob:none', '--no-checkout', 'https://github.com/lantrinh1999/Cli-Proxy-API-Management-Center.git', $panelPath)
    Invoke-CheckedNative $git @('-C', $panelPath, 'fetch', 'origin', '002f1e6a16c70ccc717387e9c46d6bec4ecc4365')
    Invoke-CheckedNative $git @('-C', $panelPath, 'checkout', '--detach', '002f1e6a16c70ccc717387e9c46d6bec4ecc4365')
}
Push-Location $panelPath
try {
    Invoke-CheckedNative $bun @('install', '--frozen-lockfile')
} finally { Pop-Location }
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'vite.local.config.ts') -Destination (Join-Path $panelPath 'vite.local.config.ts')
if ($BuildOnly) {
    Write-Host 'Build checks complete. Existing runtime was not changed.'
    return
}
Invoke-CheckedNative $python @('-c', 'import sys,pathlib; sys.path.insert(0,sys.argv[1]); from codex_client import private_directory; private_directory(pathlib.Path(sys.argv[2]))', $PSScriptRoot, $runtimeRoot)
Copy-Item -LiteralPath $serverBuild -Destination (Join-Path $runtimeRoot 'cli-proxy-api.exe')
Invoke-CheckedNative $python @((Join-Path $PSScriptRoot 'prepare_site.py'), '--workspace-root', $taskRoot)
& (Join-Path $PSScriptRoot 'start-site.ps1') -WorkspaceRoot $taskRoot
Write-Host 'Site started. Open http://127.0.0.1:5173 and use .keyrelay-runtime/ADMIN-PASSWORD.txt.'
Write-Host 'Model status: NOT READY until the separate ch-relay and Plus Auth Files login are configured.'
