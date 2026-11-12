@echo off
cd /d "%~dp0"
echo ============================================================
echo  GridPerch connectivity diagnostic (no changes are made)
echo ============================================================
echo.
set "_GPS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%_GPS%" set "_GPS=%SystemRoot%\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%_GPS%" set "_GPS=%windir%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%_GPS%" (
  echo ERROR: PowerShell not found. Expected at
  echo   %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe
  echo This host has a broken PATH or PowerShell was removed.
  pause
  exit /b 1
)
"%_GPS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0diag.ps1"
echo.
echo ==== Copy this whole output back for analysis. ====
pause
