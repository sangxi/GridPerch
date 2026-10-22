@echo off
rem ============================================================
rem  GridPerch - start monitor (server) + agent
rem  Usage: double-click this file, or run it from cmd.
rem
rem  数据与日志放在项目之外：..\opsmonitor-data\
rem  （原来的 opsmonitor.db 会被自动识别沿用，不会新建空库；发布包 releases\、日志 logs\ 也在那里）
rem ============================================================
cd /d "%~dp0"

set "DATA=..\opsmonitor-data"
if not exist "%DATA%\logs" mkdir "%DATA%\logs"

rem ---- monitor (server) ----
if not exist "bin\gridperch-server.exe" (
  echo [x] bin\gridperch-server.exe not found. Build first:
  echo     go build -o bin\gridperch-server.exe .\cmd\gridperch-server
  pause
  exit /b 1
)
netstat -ano | findstr ":9999" | findstr LISTENING >nul
if %errorlevel%==0 (
  echo [=] monitor already running on :9999
) else (
  echo [+] starting monitor on :9999 ...
  start "gridperch-server" /min cmd /c "bin\gridperch-server.exe -db %DATA%\gridperch.db > %DATA%\logs\srv.log 2>&1"
  timeout /t 3 /nobreak >nul
)

rem ---- agent ----
if not exist "bin\gridperch-agent.exe" (
  echo [x] bin\gridperch-agent.exe not found, skip agent.
  goto done
)
tasklist /FI "IMAGENAME eq gridperch-agent.exe" 2>nul | findstr /I "gridperch-agent.exe" >nul
if %errorlevel%==0 (
  echo [=] agent already running
) else (
  echo [+] starting agent ...
  start "gridperch-agent" /min cmd /c "bin\gridperch-agent.exe > %DATA%\logs\agt.log 2>&1"
)

:done
echo.
echo   console : http://127.0.0.1:9999    admin / gridperch
echo   data    : %DATA%
echo   logs    : %DATA%\logs\srv.log  (monitor)   %DATA%\logs\agt.log  (agent)
echo   stop    : run stop-gridperch.bat
echo.
timeout /t 6 /nobreak >nul
