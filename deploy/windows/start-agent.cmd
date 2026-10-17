@echo off
setlocal
rem ============================================================
rem  Strix 采集端启动脚本（Windows 7 / Server 2008 R2 兼容版）
rem
rem  用法：本文件与 strix-agent-windows-amd64-win7.exe 放在同一目录，
rem        改下面 4 行，双击本文件即可启动。
rem        要开机自启、无窗口后台常驻，见文件末尾的 schtasks 命令。
rem ============================================================

rem ── 要改的 4 项 ─────────────────────────────────────────────

rem 1) 平台地址。必须是本机能访问到的地址，不要用 127.0.0.1
set SRV=http://192.168.40.111:9999

rem 2) 通信密钥的 MD5。平台「添加主机」页生成的命令里的 wgToken 值
set TOKEN_HASH=bd53af4dfa9bd104cbbef1ce3b06dd91

rem 3) 本机在平台上显示的名字，建议用英文或数字
set HOSTNAME=web-2008r2

rem 4) 采集上报间隔（秒）
set INTERVAL=60

rem ── 以下不用改 ─────────────────────────────────────────────
set "EXE=%~dp0strix-agent-windows-amd64-win7.exe"
set "ARGS=-server %SRV% -token-hash %TOKEN_HASH% -name %HOSTNAME% -interval %INTERVAL% -update off -remote-config off"
cd /d "%~dp0"

if not exist "%EXE%" (
  echo.
  echo [错误] 找不到采集端程序：
  echo        %EXE%
  echo.
  echo        请把本脚本与 strix-agent-windows-amd64-win7.exe 放在同一目录。
  echo.
  pause
  exit /b 1
)

rem log 模式：由计划任务调用，没有控制台，输出写入 agent.log
if /i "%~1"=="log" (
  "%EXE%" %ARGS% >> "%~dp0agent.log" 2>&1
  exit /b
)

echo ============================================================
echo    Strix 采集端（Win7 / 2008 R2 兼容版）
echo    平台地址 : %SRV%
echo    主机名称 : %HOSTNAME%
echo    自动升级 : 已关闭  -update off -remote-config off
echo ------------------------------------------------------------
echo    按 Ctrl+C 停止。需要留存日志请用计划任务方式（见文件末尾）。
echo ============================================================
echo.
"%EXE%" %ARGS%
echo.
echo === 采集端已退出（退出码 %ERRORLEVEL%）===
pause
exit /b

rem ============================================================
rem  开机自启（在管理员 cmd 里执行一次即可）
rem
rem  注册。末尾的 log 参数让输出写进 agent.log
rem    schtasks /create /tn strix-agent /tr "C:\Strix\start-agent.cmd log" /sc onstart /ru SYSTEM /rl HIGHEST /f
rem  立即启动
rem    schtasks /run /tn strix-agent
rem  查看状态
rem    schtasks /query /tn strix-agent
rem  停止 / 删除
rem    schtasks /end /tn strix-agent
rem    schtasks /delete /tn strix-agent /f
rem
rem  日志文件：C:\Strix\agent.log
rem ============================================================
