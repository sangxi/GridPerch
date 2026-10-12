@echo off
rem ============================================================
rem  OpsMonitor - start monitor (server) + agent
rem  Usage: double-click this file, or run it from cmd.
rem
rem  数据与日志放在项目之外：..\opsmonitor-data\
rem  （数据库 opsmonitor.db、发布包 releases\、日志 logs\ 都在那里）
rem ============================================================
cd /d "%~dp0"

set "DATA=..\opsmonitor-data"
if not exist "%DATA%\logs" mkdir "%DATA%\logs"

rem ---- monitor (server) ----
if not exist "bin\opsmonitor-server.exe" (
  echo [x] bin\opsmonitor-server.exe not found. Build first:
  echo     go build -o bin\opsmonitor-server.exe .\cmd\opsmonitor-server
  pause
  exit /b 1
)
netstat -ano | findstr ":9999" | findstr LISTENING >nul
if %errorlevel%==0 (
  echo [=] monitor already running on :9999
) else (
  echo [+] starting monitor on :9999 ...
  start "opsmonitor-server" /min cmd /c "bin\opsmonitor-server.exe -db %DATA%\opsmonitor.db > %DATA%\logs\srv.log 2>&1"
  timeout /t 3 /nobreak >nul
)

rem ---- agent ----
if not exist "bin\opsmonitor-agent.exe" (
  echo [x] bin\opsmonitor-agent.exe not found, skip agent.
  goto done
)
tasklist /FI "IMAGENAME eq opsmonitor-agent.exe" 2>nul | findstr /I "opsmonitor-agent.exe" >nul
if %errorlevel%==0 (
  echo [=] agent already running
) else (
  echo [+] starting agent ...
  start "opsmonitor-agent" /min cmd /c "bin\opsmonitor-agent.exe > %DATA%\logs\agt.log 2>&1"
)

:done
echo.
echo   console : http://127.0.0.1:9999    admin / opsmonitor
echo   data    : %DATA%
echo   logs    : %DATA%\logs\srv.log  (monitor)   %DATA%\logs\agt.log  (agent)
echo   stop    : run stop-opsmonitor.bat
echo.
timeout /t 6 /nobreak >nul
