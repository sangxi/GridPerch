@echo off
rem ============================================================
rem  GridPerch - stop monitor (server) + agent
rem ============================================================
taskkill /F /IM gridperch-server.exe /T >nul 2>&1
taskkill /F /IM gridperch-agent.exe /T >nul 2>&1
echo [=] stopped: gridperch-server.exe / gridperch-agent.exe
timeout /t 3 /nobreak >nul
