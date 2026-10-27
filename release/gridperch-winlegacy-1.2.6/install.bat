@echo off
REM ===========================================================================
REM  GridPerch Agent - Windows Legacy (Server 2008 R2 ~ 2016) Installer Launcher
REM
REM  This file must stay pure ASCII: cmd.exe reads .bat in the OEM code page,
REM  so any UTF-8 Chinese text here would show up garbled on legacy systems.
REM
REM  Usage:
REM    1) Double-click this file. Site address and token are already built in
REM       (see the preset block at the top of install.ps1), so it installs
REM       without asking anything.
REM    2) To override, run from an ADMIN command prompt:
REM         install.bat -Server http://10.0.0.5:9999 -Token <plain token>
REM         install.bat -TokenHash <32 hex chars>
REM ===========================================================================
setlocal
title GridPerch Agent Installer

net session >nul 2>&1
if not "%errorlevel%"=="0" (
    echo Requesting administrator privileges...
    if "%~1"=="" (
        powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    ) else (
        powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -ArgumentList '%*' -Verb RunAs"
    )
    exit /b
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
if errorlevel 1 (
    echo.
    echo Installation FAILED. Read the messages above for the reason.
)

echo.
pause
