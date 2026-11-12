GridPerch 采集端离线兜底修复包  v1.2  (2026-11-10)
==========================================================

一、这个包解决什么
----------------------------------------------------------
Windows Server 2012 R2 上，在线一键安装脚本 install.ps1 走
Register-ScheduledTask 注册计划任务时，会静默忽略「不限时运行」和
「SYSTEM 主体」两项设置，结果任务仍是 72 小时强杀（PT72H），
装完两三天又集体掉线；而 taskschd 的手写 XML 回落路径在这批机器上
又会报「任务 XML 格式不正确或超出范围的值」。

本包完全离线，不依赖服务端 9999 端口下载安装脚本，直接：
  1) 用包内自带的兼容版 agent.exe（1.2.13 legacy，Go 1.20 构建，
     支持 2008 R2 / 2012 R2）覆盖 C:\GridPerch\gridperch-agent.exe；
  2) 通过 Task Scheduler COM 接口直接创建/改写任务定义，强制设置
     ExecutionTimeLimit = PT0S（不限时）、SYSTEM 主体、开机触发、
     失败自动重启、StartWhenAvailable；
  3) 启动采集端并回显任务配置，供现场核验。

二、两个入口（在目标机上，右键「以管理员身份运行」）
----------------------------------------------------------
  install.bat    完整模式：换 exe、清旧任务、重建任务、启动采集端。
                 用于那 9 台离线需要重装的机器。

  fix-task.bat   仅修任务：不换 exe、不杀正在上报的进程，只把已有任务
                 的时限/主体改对。用于「已经装好但任务还是 PT72H」的机器。

  ★ .94 楚雄虹城服务器已经修好（PT0S + SYSTEM），不需要再跑这个包。

三、使用步骤
----------------------------------------------------------
  1. 把整个文件夹（或解压后的目录）拷贝到目标机，例如 C:\gpfix\。
     路径里不要带中文和空格。
  2. 右键 install.bat →「以管理员身份运行」。
  3. 等窗口跑完（有 pause，不会自动关闭），看最后 == VERIFY == 部分。
  4. 5 分钟内到监控控制台确认该主机恢复在线。
     若仍显示离线，右键 diag.bat →「以管理员身份运行」，把整屏输出
     复制回来。这是只读诊断，不做任何修改：会查 agent 进程与命令行、
     agent.log 末尾 15 行、到服务端 22/80/443/9999 的 TCP 可达性、
     Windows 防火墙出站 Block 规则、杀软/EDR、路由、任务时限。
     （最常见原因：到 9999 的出站被本机安全策略或网络 ACL 拦住，
       此时包虽装好，采集端也发不出数据。）

四、成功标准（== VERIFY == 里必须出现）
----------------------------------------------------------
  <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  <UserId>SYSTEM</UserId>          （SYSTEM 等价 S-1-5-18，两者都对）
  <Command>C:\GridPerch\gridperch-agent.exe</Command>
  AGENT_PROCS=1
  RESULT=OK: unlimited runtime, no 72h kill

  若出现 RESULT=FAIL 或仍是 PT72H，把整个窗口输出复制回来即可。

五、主机名怎么来的
----------------------------------------------------------
  脚本按本机 IPv4 查 hosts.txt（IP,主机名）自动选名字，一个包可以
  拷到所有机器通用，不用逐台改配置。若某台 IP 不在表里，会依次回退到
  「正在运行的 agent 命令行里的 -name」「本机 COMPUTERNAME」。
  新增机器只需在 hosts.txt 里加一行「IP,主机名」。

六、注意事项
----------------------------------------------------------
  - 必须以管理员身份运行；否则第一行的 FATAL 会直接退出。
  - 若双击 bat 报「'powershell.exe' 不是内部或外部命令」，说明该机 PATH
    里缺 System32。v1.1 起三个 bat 都用绝对路径调用 PowerShell，且 ps1
    会在进程内自动修复 PATH（保证 schtasks / netsh / route 也能找到），
    不需要手工改环境变量。若确实找不到 PowerShell，bat 会给出明确报错。
  - 旧系统必须带 -update off -remote-config off（脚本已写死），
    否则自动升级会换上 Go 1.23 的正式版并在旧系统上闪退。
  - .168 新零售应用数据服务器：出站 9999 曾被拦，后来已放通，
    2026-11-10 已恢复在线且任务为 PT0S。
  - .159 推广零售服务器-关机-：机器关机，等开机后再跑。
  - 部分旧机器 PATH 里没有 powershell.exe。若要在「主机本地执行」
    里远程调用 PowerShell，必须写绝对路径：
      %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe
  - 部分 2008 R2 上直接 schtasks /Query /XML 会报「无法加载列资源」，
    这是 schtasks 本地化资源的老问题，命令前加 chcp 437 >nul 即可。
  - 包会删除名称含 gridperch / strix / opsmonitor 的旧计划任务和进程
    （仅完整模式），这是为了清掉改名前的老任务，属预期行为。
  - 不改服务端、不重启监控平台、不需要发版。

七、文件清单
----------------------------------------------------------
  install.bat          完整模式入口（ASCII / CRLF）
  fix-task.bat         仅修任务入口（ASCII / CRLF）
  diag.bat             连通性诊断入口（只读，ASCII / CRLF）
  diag.ps1             诊断脚本（全 ASCII，只读，PowerShell 2.0 兼容）
  gridperch-fix.ps1    主脚本（全 ASCII，PowerShell 2.0 兼容）
  hosts.txt            IP → 主机名映射（UTF-8 BOM）
  agent.exe            兼容版采集端 1.2.13，SHA256 39a6547f…
  README.txt           本说明（UTF-8 BOM）

  服务端：http://192.168.40.111:9999
  通信密钥 MD5：ffb977235c34019e5a27f12639a21126

八、新增其它机器怎么加
----------------------------------------------------------
  1. 在 hosts.txt 末尾加一行，格式「IP,主机名」，例如：
       192.168.40.200,新药房服务器
     主机名必须与控制台「主机监控」里显示的完全一致（含中文、大小写、
     中划线），否则会上报成一个新名字，主机表里多出一台。
     逗号前后不要空格，一行一台。
  2. 保存必须是 UTF-8（带 BOM）、CRLF 换行。中文若用 ANSI 保存会乱码，
     导致 name 对不上。推荐先 cd 到本目录，再在 PowerShell 里直接粘：
       $p='.\hosts.txt'; $l=[IO.File]::ReadAllLines($p,[Text.Encoding]::UTF8); $l+='192.168.40.200,新药房服务器'; [IO.File]::WriteAllLines($p,$l,(New-Object Text.UTF8Encoding($true)))
     这条命令能自动保持 BOM 与 CRLF。它本身含中文，请在交互式
     PowerShell 里粘贴执行，不要另存成无 BOM 的 .ps1 再运行。
  3. 不需要重新打包：直接在目标机的 hosts.txt 上加即可；若要统一分发，
     改完把整个文件夹重新压缩一次。
  4. 把文件夹拷到新机器（路径无中文无空格），右键管理员运行
     install.bat，看 == VERIFY == 是否为 PT0S / SYSTEM / RESULT=OK。

  ★ 主要给 Windows Server 2012 R2 用（72 小时雷集中在这一组）。
    2008 R2 绝大多数走 XML 路径本来就是 PT0S，一般不用跑；
    但 2026-11-10 全量核验发现 1 台 2008 R2（.182 TMS应用服务器）
    仍是 PT72H，已远程修好 —— 所以不要凭操作系统下结论，按第九节
    远程核验一遍最稳妥，只有查出 PT72H 的机器才需要处理。
    Win10 / Server 2016+ / Linux 千万不要用本包 —— 包内 agent.exe 是
    Go 1.20 构建的旧系统兼容版，给新系统装会把版本降回去，
    新系统请用控制台的在线一键安装命令。
    ★ 包内只有一个 agent.exe，2008 R2 与 2012 R2 通用，
      没有、也不需要单独的「2008 包」。

  ★ 查不到 IP 时脚本会依次回退「运行中 agent 的 -name」→ 本机
    COMPUTERNAME；用 COMPUTERNAME 上报同样会多出一台新主机，
    所以新机器务必先加进 hosts.txt。

九、不用上机，远程批量核验任务配置（2026-11-10 新增）
----------------------------------------------------------
  控制台「工具箱 → 主机本地执行」下发的指令由采集端在本机执行，
  5 秒拉一次，几秒就回包。逐台贴下面这条（只读，不改任何东西）：

    chcp 437 >nul & schtasks /Query /TN gridperch-agent /XML

  回包里看三项，缺一不可：
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>   不是 PT72H
    <UserId>SYSTEM</UserId> 或 S-1-5-18
    <Enabled>true</Enabled>   （Settings 段里的那个，注意别看
                              BootTrigger 段的同名标签）

  2026-11-10 全量核验结果（36 台在线旧系统）：
    2012 R2  11 台：全部 PT0S + SYSTEM；其中 .94 的任务是「已禁用」
                     （见下），其余 10 台 Enabled=true。
    2008 R2  25 台：修前 23 台 PT0S、.182 为 PT72H、.151 当次查询
                     超时；复核后 .151 为 PT0S，.182 已远程改成 PT0S，
                     现 25/25 全部 PT0S + 启用。
    唯一离线：.159（关机），开机后必须先核验再决定是否跑包。

  ★ 遗留项：.94 楚雄虹城服务器。任务本身已是 PT0S + SYSTEM，但状态
    一直是「已禁用」。远程用 schtasks /Change /ENABLE、COM 属性、
    Enable-ScheduledTask cmdlet、删除后重建四种方式试过，同进程内
    都显示成功，换一个新进程读回仍是 Disabled，说明这台机器的
    计划任务启用状态没有被持久化（疑似计划任务服务/安全策略回写）。
    当前 agent 进程正常在跑、主机在线，但重启后可能不会自动拉起。
    处理：在有管理员会话的控制台上跑一次本包 install.bat，
    或检查本机是否有安全软件/组策略在禁用该任务。
    其余 10 台 2012 R2 无此现象。
