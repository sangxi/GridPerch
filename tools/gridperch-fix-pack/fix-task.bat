@echo off
cd /d "%~dp0"
echo ============================================================
echo  GridPerch offline repair pack - FIX TASK ONLY
echo  Keeps the running agent; only rewrites task settings/principal
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
"%_GPS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0gridperch-fix.ps1" -FixOnly
echo.
echo ==== Finished. Copy this window output if anything failed. ====
pause
