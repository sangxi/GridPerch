@echo off
rem ============================================================
rem  Strix - stop monitor (server) + agent
rem ============================================================
taskkill /F /IM strix-server.exe /T >nul 2>&1
taskkill /F /IM strix-agent.exe /T >nul 2>&1
echo [=] stopped: strix-server.exe / strix-agent.exe
timeout /t 3 /nobreak >nul
