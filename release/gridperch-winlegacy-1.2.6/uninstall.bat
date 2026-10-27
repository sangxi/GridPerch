@echo off
REM ===========================================================================
REM  GridPerch Agent - Uninstaller for Windows legacy systems
REM
REM  Pure ASCII on purpose (see install.bat).
REM  Usage:  double-click, or run from an admin prompt:
REM            uninstall.bat "D:\MyPath"
REM          (argument = install directory, default C:\GridPerch)
REM ===========================================================================
setlocal
title GridPerch Agent Uninstaller

net session >nul 2>&1
if not "%errorlevel%"=="0" (
    echo Requesting administrator privileges...
    powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -ArgumentList '%*' -Verb RunAs"
    exit /b
)

set "TASKNAME=gridperch-agent"
set "INSTDIR=%~1"
if "%INSTDIR%"=="" set "INSTDIR=C:\GridPerch"

echo.
echo ==^> Stopping scheduled task
schtasks /End /TN "%TASKNAME%" >nul 2>&1

echo ==^> Deleting scheduled task
schtasks /Delete /TN "%TASKNAME%" /F >nul 2>&1

echo ==^> Stopping running process
taskkill /IM gridperch-agent.exe /F >nul 2>&1

REM Give Windows a moment to release file handles before deleting the folder
ping -n 3 127.0.0.1 >nul

echo ==^> Removing install directory: %INSTDIR%
if exist "%INSTDIR%" (
    rmdir /S /Q "%INSTDIR%"
)

if exist "%INSTDIR%" (
    echo.
    echo WARNING: directory still exists. Some files may be locked.
    echo Close any open window in that folder and delete it manually.
) else (
    echo.
    echo Uninstall completed.
    echo NOTE: the host entry in the GridPerch console is NOT removed.
    echo       Delete it from the console if you no longer need it.
)

echo.
pause
