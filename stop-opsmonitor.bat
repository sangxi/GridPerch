@echo off
rem ============================================================
rem  OpsMonitor - stop monitor (server) + agent
rem ============================================================
taskkill /F /IM opsmonitor-server.exe /T >nul 2>&1
taskkill /F /IM opsmonitor-agent.exe /T >nul 2>&1
echo [=] stopped: opsmonitor-server.exe / opsmonitor-agent.exe
timeout /t 3 /nobreak >nul
