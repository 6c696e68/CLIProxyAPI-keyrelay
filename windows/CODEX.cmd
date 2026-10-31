@echo off
setlocal
chcp 65001 >nul
set "PYTHONUTF8=1"
python "%~dp0codex_client.py" %*
if errorlevel 1 pause
endlocal
