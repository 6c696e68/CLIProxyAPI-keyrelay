@echo off
setlocal
chcp 65001 >nul
set "PYTHONUTF8=1"
python "%~dp0relay_manager.py" login %*
if errorlevel 1 pause
endlocal
