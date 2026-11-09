# sing-box panel

面向低配设备（MT7981 / 256 MB）的 sing-box Web 控制面：**单二进制、前端 embed、常驻内存 16 MB（堆 1–2 MB）**。
内置 **sing-box 与 mihomo 双内核**，可在面板里一键切换。
已在一台 Debian 13 x86_64 服务器上用两个真实机场订阅完成端到端验证。

## 打开面板：http://127.0.0.1:9090

面板**默认监听 `127.0.0.1:9090`**，浏览器直接开 <http://127.0.0.1:9090>，
首次启动打开面板网页，按引导设置登录账户密码（panel.conf 填 password= 可跳过），
或者用 `-password` 自己指定：

```sh
./panel -listen 127.0.0.1:9090 -password your-pass
```

> 数据目录默认在 exe 同目录的 `panel-data`（Linux 安装版为
> `/opt/sing-box-panel/data`），可用 `-data` 覆盖；也可在 panel.conf 里写
> `data=<路径>` 固化。首次启动时若数据目录为空，日志会明确提示（全新安装），
> 避免换目录启动时误以为数据丢失。

所有端口一览（都能在面板里改）：

| 用途 | 默认地址 | 改法 |
| --- | --- | --- |
| **面板 Web / API** | **`127.0.0.1:9090`** | `-listen` 参数或 `panel.conf` 的 `listen=` |
| Clash API（面板内部读连接/延迟） | `127.0.0.1:9091` | `-clash` 参数 |
| 本机代理入口（系统代理填这个） | `127.0.0.1:7890` | 面板「设置 → 本机端口」 |
| 局域网 SOCKS5 / HTTP | `0.0.0.0:1080` / `0.0.0.0:8118` | 「局域网代理」页，默认关闭 |
| Linux 透明代理 redirect | `0.0.0.0:12345` | 「透明代理」页 |
| Linux 局域网 DNS 劫持 | `0.0.0.0:5354` | 同上（随透明代理开启） |

端口优先级：**命令行 `-listen` > `panel.conf` 的 `listen=` > 内置默认 `127.0.0.1:9090`**。
想从局域网访问就改成 `0.0.0.0:9090`（注意同时设置强密码，或只在内网用）。

## 下载即用（推荐）

到 [Releases](https://github.com/jiqing112/sing-box-web/releases) 下载对应平台的包，
里面**已经带好了对应平台的两个内核**（sing-box + mihomo），解压就能跑，
不需要装 Go、不需要自己找内核。

| 平台 | 文件 | 随包内核 | 用法 |
| --- | --- | --- | --- |
| Windows x64 | `sing-box-web-*-windows-amd64-dual.zip` | sing-box + mihomo | 解压 → 双击 `start.bat` → 浏览器自动打开面板 |
| Linux x64 | `sing-box-web-*-linux-amd64-dual.tar.gz` | sing-box + mihomo | 解压 → `sudo ./install.sh` |
| Linux arm64 | `sing-box-web-*-linux-arm64-dual.tar.gz` | sing-box + mihomo | ARM 路由器 / 小主机（同上） |
| Linux mipsle | `sing-box-web-*-linux-mipsle-with-sing-box.tar.gz` | 仅 sing-box | MT7621 / MT7981 小 ROM 路由，纯静态 |
| Linux mipsle | `sing-box-web-*-linux-mipsle-with-mihomo.tar.gz` | 仅 mihomo | 同上，偏好 mihomo 的 |

**包名即内容**：`-dual` = 双内核都随包（x86/ARM 大存储设备）；`-with-sing-box` /
`-with-mihomo` = 单内核路由器包——双内核解压 150MB+，128MB 级 ROM 放不下；两个
mipsle 包里的 panel 二进制完全相同，只随附的内核不同，换内核换包即可。

- 每个包都附 `.sha256` 校验文件。
- 首次启动打开网页会要求设置登录账户密码；自动化部署在 panel.conf 填
  `password=` 即跳过网页初始化。
- 包里的内核版本随发布固定（当前 sing-box **1.14.3** / mihomo **1.19.33**），
  面板会自动在同目录 / `/usr/local/bin` 找到它们。
- 发新版时 GitHub Actions 会自动重新下载两个内核并打包，见 `.github/workflows/release.yml`。

## 两个内核：sing-box ↔ mihomo

默认用 sing-box，切换入口在 **设置 → 内核**，点一下整套流程自动走完。

| | sing-box | mihomo |
| --- | --- | --- |
| 配置文件 | `config.json`（JSON） | `config.yaml`（Clash 系 YAML） |
| 模板 | `template.json` | `template.yaml` |
| 校验 | `sing-box check -c config.json` | `mihomo -t -d <data> -f config.yaml` |
| 启动 | `sing-box run -c config.json` | `mihomo -d <data>/mihomo -f config.yaml` |
| 工作目录 | 数据目录 | `data/mihomo/`（独立，避免和 sing-box 抢 `cache.db`） |
| Linux 透明代理 | `redirect` 入站（`0.0.0.0:12345`） | `listeners` 里 `type: redir`（`0.0.0.0:12345`） |
| TUN 网卡 | `singbox-tun` | `mihomo-tun` |

设计要点：

- **节点只存一份**。订阅解析结果统一存成 sing-box outbound 片段（`nodes-*.json`），
  渲染 mihomo 配置时翻译成 Clash `proxies`。所以订阅解析、测速、选节点两个内核行为一致。
- **配置互不覆盖**。两套模板、两套配置、两套快照（`config.json` / `config.yaml`），
  切过去再切回来，原来那份还在。
- **切换是事务性的**。停旧核心 → 渲染新配置 → 校验 → 启动 → 探活，
  任一步失败就把 store / 子进程 / 二进制路径全部回退，并写回旧配置、重启旧核心。
  切换后如果透明代理规则不再成立（比如端口没人监听），会自动关掉透明代理并提示，
  不会留下「规则还在但整网断掉」的状态。
- **geo 数据提前备好，切内核不用等**。mihomo 在工作目录里找不到 `GeoSite.dat` /
  `geoip.metadb` 时会自己去下载，实测要 31 秒（GeoSite 5 s + MMDB 25 s），这段时间
  切换请求一直挂着。面板的做法是三层兜底：发布包里 embed 一份（`geodata/fetch.sh`
  打包，零下载、离线可用）→ 面板启动和切到 sing-box 时后台预取 → 切到 mihomo 前
  再 `Ensure` 一次。实测切换从 21.2 s 降到 **0.9 s**，缺文件时也能自己补上。
  两个文件并行下载，单源 8 秒内低于 48 KB/s 自动换镜像（虚拟机 NAT 后面 jsDelivr
  只有 75 KB/s，gh-proxy 有 2 MB/s）。
  校验超时也按内核区分（sing-box 15s / mihomo 90s，后者是给「预取失败」留的兜底）。
- **代理组名与 sing-box 对齐**（`proxy` / `auto`），面板读延迟、切节点不用区分内核。

命令行：

```bash
panel -engine mihomo                       # 强制用 mihomo（覆盖面板里存的选择）
panel -sing-box /usr/local/bin/sing-box \
      -mihomo  /usr/local/bin/mihomo       # 自定义内核路径
panel -clash 127.0.0.1:9091                # Clash API 地址（面板读连接/延迟靠它）
```

启动日志会打印当前内核与二进制路径；`GET /api/v1/engine` 会返回两个内核各自的
可用性、版本与错误原因，前端「设置 → 内核」直接用它渲染。

## 仓库结构

| 路径 | 内容 |
| --- | --- |
| `app/` | **可编译的成品**：Go 后端 + embed 前端 + systemd 单元 |
| `app/internal/service/engine/` | 内核抽象层：文件名、扩展名、命令行参数、工作目录、版本探测 |
| `app/internal/service/config/clash.go` | sing-box 节点 → Clash proxy 翻译、mihomo 配置渲染 |
| `app/deploy/` | 发布包用的部署文件：`panel.conf` 模板、`install.sh`、`start.bat`、systemd 单元 |
| `.github/workflows/release.yml` | 打 tag 自动编译三平台 + 下载两个内核 + 打包发布 |
| `web/` | 设计原型（纯静态、模拟数据），用于评审交互与视觉 |
| `docs/index.html` | 架构设计文档：模块划分、数据流、接口清单、P0 代码骨架、OOM 风险 Top 5 |

> 截图不在仓库里（面板截图会带真实订阅地址），需要看图可以直接跑起来看，或见 `docs/index.html` 的设计稿。

## 快速开始（本地构建）

```bash
cd app
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o panel ./cmd/panel
```

产物约 **6.7 MB**，内含全部前端资源（`//go:embed all:dist`），运行时不需要 Node。

```bash
./panel -listen 127.0.0.1:9090 -password your-pass
```

首次启动会生成 `data/settings.json`、`data/template.json`（用 mihomo 时是 `data/template.yaml`）；
`data/config.json` / `data/config.yaml` 要等你第一次在「配置」页保存应用（或触发一次需要重渲染的开关）后才会写入。

下文出现的 `data/` 一律指 **`-data` 指定的数据目录**，不是系统根目录的 `/data`：
Linux 默认 `/opt/sing-box-panel/data`，Windows 默认 exe 同级的 `panel-data\`。

## Windows

```powershell
cd app
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 `
  go build -trimpath -ldflags="-s -w" -o ..\panel-windows-amd64.exe ./cmd/panel
```

- 不带 `-data` 时，数据目录默认是 exe 旁边的 `panel-data\`。

### 系统托盘与管理脚本

- **系统托盘**：panel.exe 运行时任务栏右下角有绿菱形托盘图标——**双击打开网页配置**，
  右键菜单：打开面板 / 启动·停止·重启核心 / 开机自启开关 / 退出（优雅退出，自动停核心）。
  纯 `syscall` 实现 Shell_NotifyIconW，**零新增依赖**；`-notray` 可关闭；
  SYSTEM 计划任务自启运行在会话 0，无桌面无托盘，属正常现象。
- **代理模式页**（仅 Windows；Linux 同页为「透明代理」redirect）：
  - **出口策略：路由模式**——规则分流 / 全局代理 / 全局直连。注意它只对
    **进入核心的流量**生效，入口没开时普通流量根本不进核心。
  - **流量入口**（两者至少开其一）：
    - **系统代理**：一键把当前登录用户的 Windows 系统代理指向面板 mixed 端口
      （`127.0.0.1:7890`，端口随设置联动）。写完广播 WinInet 刷新，Edge/Chrome
      **免重启即时生效**；关闭自动还原开启前原值（原 ProxyEnable/Server/Override
      存于 `panel-data/sysproxy-state.json`）。面板以 SYSTEM 计划任务运行时，
      自动定位到交互会话真实用户 hive 写入（console 无登录用户时枚举 RDP Active
      会话），日志明确记录写到哪个用户；普通用户面板直写 HKCU。
    - **TUN 虚拟网卡**：接管整机流量（含不走系统代理的程序），需管理员。
  - **状态提示条**：四态实时提示——全关（警示「路由模式不生效」）/ 仅系统代理 /
    仅 TUN / 双开（提示冗余）。
- **panel.bat 管理脚本**（发行包内）：双击 = 智能默认——未运行则后台隐藏启动（无黑窗，
  仅托盘 + 自动开网页），已运行则弹管理菜单；命令行支持 `start / startbg / stop /
  restart / status / open / autostart-on|off / menu`。自启开关与面板网页操作的是同一个
  计划任务 `sing-box-panel`，状态互通；`stop` 按数据目录匹配内核，不误伤其他实例。
  脚本为 GBK 编码（中文 Windows 默认控制台直接可用），编辑后用 `tools/bat_to_gbk.py` 转码。

### 内核 exe 的三种部署方式

面板按以下顺序定位内核（`sing-box.exe` / `mihomo.exe` 各自独立解析），任选其一：

1. **同目录（推荐）**：把官方 `sing-box.exe`、`mihomo.exe` 放在面板 exe 旁边，零配置自动发现；
2. **加入 PATH**：面板找不到同目录的 exe 时，回退为按 `PATH` 解析同名程序；
3. **启动参数显式指定**：`panel-windows-amd64.exe -sing-box "D:\tools\sing-box\sing-box.exe" -mihomo "D:\tools\mihomo\mihomo.exe"`。

方式 3 需要同步修改计划任务/快捷方式的启动参数；方式 2 需要确认面板进程的 PATH 环境
（计划任务以 SYSTEM 运行时，用的是系统 PATH 而不是用户 PATH）。
mihomo 是可选的，没有也不影响面板启动，只是「设置 → 内核」里它显示不可用。

- **「代理模式」菜单双平台复用**：Linux 显示 nftables redirect 接管（含局域网 DNS/本机
  接管开关、nftables 规则预览）；Windows 同一菜单为「代理模式」（系统代理/TUN 入口 + 路由出口），
  两边的「局域网代理」设置都可在这页管理。tproxy API 在 Windows 上仍返回明确的
  unsupported 错误；默认模板也不会生成 sing-box 在 Windows 上无法运行的 `redirect` /
  53 端口 DNS 入站（避免核心崩溃循环）。
- **Windows 全局接管用 TUN**：「代理模式」页一键开关（`GET/PUT /api/v1/tun`）。
  服务页不再重复放一张 TUN 卡（同一状态在两处显示只会让人猜哪个是真的）。
  面板必须以**管理员身份**运行（子进程继承令牌才能创建虚拟网卡：sing-box 用 `singbox-tun`，
  mihomo 用 `mihomo-tun`；mihomo 自带 wintun，不需要额外放 dll）；
  开启后整机流量（包括不走系统代理的程序）经 `auto_route` 进入内核。开关会重新渲染
  配置并重启核心，失败自动回滚。
- **路由模式**：「代理模式 / 透明代理」页提供三档——规则分流（国内直连、其余走节点）、
  全局代理（包括国内流量在内全部走节点）、全局直连。切换会重渲染配置并重启核心，失败自动回滚。
- Windows 上请使用入站代理：`mixed`（默认 `127.0.0.1:7890`），或在「局域网代理」里开启
  带鉴权的 SOCKS5/HTTP 端口。
- 核心由面板以隐藏窗口的独立进程组拉起，停止时通过 `taskkill /T /F` 整树终止，无残留进程；
  面板启动时会按可执行文件路径清扫上次强退遗留的核心孤儿（避免 cache.db 文件锁死锁）。
- 面板与核心的内存占用通过 psapi `GetProcessMemoryInfo` 读取，与任务管理器一致。

### panel.conf 配置文件（端口 / 账户 / 密码）

首次启动会在 **exe 旁边**自动生成带注释的 `panel.conf` 模板，直接编辑并重启面板即可生效，
无需改计划任务参数：

```ini
# 面板监听地址，例如 127.0.0.1:9090（仅本机访问）或 0.0.0.0:9090（局域网可访问）
listen=127.0.0.1:9090
# 登录用户名/密码；两项都留空表示关闭登录，打开端口直接进入首页
# 只清空 password 会保持现有密码不变；修改用户名时必须同时填写 password
username=admin
# 此文件为明文，请注意保管
password=
```

- **优先级**：命令行参数 > `panel.conf` > 内置默认（`127.0.0.1:9090`）。
- 改 `password` 重启后即生效（可与当前密码相同则无变化）；这也是**忘记密码时的重置入口**。
- 改 `username` 必须同时填写 `password`（登录哈希以「用户名:密码」为基，单独改用户名会被忽略并记录日志）。
- 配置随目录一起迁移，`-data` 显式指定数据目录时不影响本文件的读取位置。

**面板内修改监听地址**：打开「设置 → 运行参数 → 监听地址」，填写 `host:port`（例如
`0.0.0.0:9090`）后点保存。面板会先绑定新地址再关闭旧监听，无需重启进程（同端口只改
host 时先关后绑，失败自动回滚）；同时只回写 `panel.conf` 的 `listen=` 行，注释和其他
配置保持不变。若启动命令显式带了 `-listen`，设置页会显示固定提示：修改对当前进程立即
生效，但重启后仍以启动参数为准——要永久改端口，请同时修改计划任务/unit 或去掉 `-listen`。

**关闭登录（免密码访问）**：把 `username` 和 `password` 两行都留空并重启，面板会跳过登录页，
打开端口直接进入首页；此时 API/WebSocket 也不再校验 Token。

```ini
username=
password=
```

也可以在面板「设置 → 访问控制」中打开「允许免密码访问」，无需改配置文件。
开关状态保存在 `settings.json` 中，重启后保持；重新启用登录时必须设置新的用户名与
至少 8 位密码，保存后其他已登录会话立即失效。

- `panel.conf` 两项显式留空优先：重启后仍是免登录；
- 想用 `panel.conf` 恢复登录，填写非空 `password` 即可（会同时清除免登录开关）；
- 面板内关闭登录不会删除已保存的密码哈希，重新启用时仍要求重设新密码。

> 警告：免登录等于把面板的完整控制权交给任何能访问该端口的人。仅建议监听
> `127.0.0.1` 或可信内网时使用，并配合防火墙限制来源；只清空 `password`
> 不会关闭登录，仍保持原密码。

```powershell
.\panel-windows-amd64.exe -data .\panel-data -listen 127.0.0.1:9090 -password your-pass
```

### 开机自启（可选）

注册为计划任务（SYSTEM + 最高权限，开机即带 TUN；失败 1 分钟后自动重启，无时长限制）：

```powershell
$action    = New-ScheduledTaskAction -Execute 'C:\path\to\panel-windows-amd64.exe'
$trigger   = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$ts        = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
               -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName 'sing-box-panel' -Action $action -Trigger $trigger `
               -Principal $principal -Settings $ts -Force
# 立即启动：Start-ScheduledTask sing-box-panel；删除：Unregister-ScheduledTask sing-box-panel -Confirm:$false
```

注意：计划任务的 `-Execute` 绑定 exe 的**绝对路径**，目录搬迁后按下面迁移说明重新注册；
若 sing-box.exe 不在同目录也不在 PATH，`-Action` 里加 `Argument: '-sing-box <绝对路径>'`。

### 迁移注意事项

- **整个目录一起搬**：`panel-windows-amd64.exe` + `sing-box.exe` + `panel-data\` 三者保持
  相对关系不变，拷走即完成迁移，无需任何参数。
- **单独挪动目录后必须重新应用一次配置**：渲染出的 `config.json` 里 `cache_file.path` 是
  **绝对路径**（相对路径的解析基准是核心子进程的工作目录，跨启动方式不可靠）。搬完目录后在
  「配置」页重新保存应用一次（或任意开关一次 TUN/局域网代理），cache.db 路径即指向新位置。
- **开机自启的计划任务绑定 exe 路径**：移动目录后按上面「开机自启」的命令重新注册
  （`-Force` 覆盖同名任务即可）。
- `-data` 显式指定数据目录时不受 exe 位置影响，但同样注意 cache 路径的重渲染。

## 发布与自动构建（GitHub Actions）

仓库自带 CI（[`.github/workflows/release.yml`](.github/workflows/release.yml)）：

- **发版**：推送 `v*` 标签即自动构建全部平台并发布 Release——

  ```bash
  git tag v0.9.12 && git push origin v0.9.12
  ```

  完成后在 [Releases](https://github.com/jiqing112/sing-box-web/releases) 页面拿到各平台
  `tar.gz / zip` 与 `.sha256` 校验文件，Release Notes 自动生成。
- **手动发版**：Actions 页 `release` 工作流支持 `workflow_dispatch`，可以自定义
  版本号与随包分发的 sing-box / mihomo 版本（不推标签也能发）。
- **产物矩阵**：

  | 产物 | 面板 | 内置内核 | 适用 |
  | --- | --- | --- | --- |
  | `*-linux-amd64.tar.gz` | x86_64 纯静态 | sing-box + mihomo | x86 服务器 / NAS / 软路由 |
  | `*-linux-arm64.tar.gz` | arm64 纯静态 | sing-box + mihomo | MT7981（AX3000 系）、ARM 盒子 |
  | `*-linux-mipsle.tar.gz` | mipsle 软浮点纯静态 | sing-box + mihomo (softfloat) | MT7621（Cudy AC1200 等 MIPS 老路由） |
  | `*-windows-amd64.zip` | x64 | sing-box.exe + mihomo.exe | Windows（TUN 需管理员） |

  面板二进制由 `CGO_ENABLED=0` 编译，**纯静态零依赖**（mipsle 也不需要系统里有
  musl/libgcc），U 盘、extroot、改过 libc 的固件都能直接跑。
- **geo 数据内嵌**：CI 打包时会下载 GeoSite.dat / geoip.metadb 并 gzip 后 embed 进面板，
  切到 mihomo 内核时零下载、离线可用（CDN 失败时自动降级为运行期下载，不阻塞发布）。
- **升级内核版本**：手动触发时填 `singbox_version` / `mihomo_version` 参数即可，
  或改 workflow 顶部的默认值。

## 部署指南（事无巨细版）

> 所有流程都在真实环境验证过：Debian 13 服务器（x86_64，双真实机场订阅端到端）、
> Debian 小主机（Go 版数据迁移）。命令可直接复制执行，版本号换成 Release 页最新即可。

### 第 0 步：选对包

在目标机执行 `uname -m` 对号入座：

| `uname -m` 输出 | 下载包 | 典型设备 |
| --- | --- | --- |
| `x86_64` | `*-linux-amd64.tar.gz` | x86 服务器 / NAS / 软路由 |
| `aarch64` | `*-linux-arm64.tar.gz` | MT7981（红米 AX3000、CR880x 等）、ARM 盒子 |
| `mipsel` | `*-linux-mipsle.tar.gz` | MT7621（Cudy AC1200、小米 mini 等 MIPS 老路由） |
| （Windows） | `*-windows-amd64.zip` | Windows 10/11 x64 |

包内清单：`panel`（面板本体，前端已内嵌）、`sing-box` / `mihomo`（双内核）、
`install.sh`、`panel.conf`、`sing-box-panel.service`、README（Windows 包是 `start.bat`）。

**端口占用总表**（面板里都能改）：

| 端口 | 用途 | 默认监听 |
| --- | --- | --- |
| 9090 | 面板 Web | `127.0.0.1`（conf/参数可改 `0.0.0.0`） |
| 9091 | Clash API（面板内部读状态/测速） | 仅 `127.0.0.1` |
| 7890 | mixed 入站（系统代理） | 仅 `127.0.0.1` |
| 12345 | 透明代理 redirect 入站 | `0.0.0.0` |
| 5354 | 局域网 DNS 劫持入站 | `0.0.0.0` |
| 1080 / 8118 | 局域网 SOCKS5 / HTTP 代理 | `0.0.0.0`（开关开启时） |

### 场景 A：Debian / Ubuntu 服务器（amd64 / arm64）

#### A1. 下载与校验

```bash
curl -fsSL -o /tmp/panel.tgz \
  https://github.com/jiqing112/sing-box-web/releases/download/v0.9.12/sing-box-web-v0.9.12-linux-amd64-dual.tar.gz

# 校验完整性（可选但建议）
curl -fsSL -o /tmp/panel.tgz.sha256 \
  https://github.com/jiqing112/sing-box-web/releases/download/v0.9.12/sing-box-web-v0.9.12-linux-amd64-dual.tar.gz.sha256
cd /tmp && sha256sum -c panel.tgz.sha256
# 预期输出: sing-box-web-v0.9.12-linux-amd64-dual.tar.gz: OK

tar -xzf /tmp/panel.tgz -C /tmp && cd /tmp/sing-box-web-v0.9.12-linux-amd64-dual
ls
# 预期: install.sh  mihomo  panel  panel.conf  README.txt  sing-box  sing-box-panel.service
```

#### A2. 安装

```bash
sudo ./install.sh
```

脚本做的事（每步都有输出）：

1. 停掉已在运行的旧 `sing-box-panel` 服务（如有）；
2. `panel` → `/opt/sing-box-panel/bin/panel`，数据目录 `/opt/sing-box-panel/data`；
3. `sing-box` → `/usr/local/bin/sing-box`，`mihomo` → `/usr/local/bin/mihomo`；
4. `panel.conf` 只在缺失时安装（不会覆盖你改过的配置）；
5. 写入并启用 systemd 服务 `sing-box-panel`（开机自启）；
6. 打印面板地址和初始密码。

#### A3. 首次登录

- 浏览器打开 `http://<本机IP>:9090`，用户名 `admin`。
- 密码从哪来（优先级从高到低）：
  1. systemd unit 里显式传的 `PANEL_PASSWORD`（默认注释掉）；
  2. `panel.conf` 里写了 `password=xxx` → 用它；
  3. 启动参数 `-password xxx` 或环境变量 `PANEL_PASSWORD`；
  4. 都没给 → 自动生成随机密码，**安装输出里直接打印**，同时落在
     打开网页按引导完成首次账户设置；或在 panel.conf 填 `password=` 后重启。
- 登录后请到「设置 → 访问控制」改掉密码。
- 忘记密码：编辑 `/opt/sing-box-panel/bin/panel.conf`，填上 `username=admin` 和
  新 `password=xxx`，`systemctl restart sing-box-panel` 即完成重置。

#### A4. 第一次上手（面板操作顺序）

1. **订阅页** → 添加订阅：粘贴机场订阅链接，自动识别 Clash YAML / Base64 URI / sing-box JSON；
   卡片上会显示流量用量与到期时间。
   **单击订阅卡片**可编辑名称、订阅链接、自动更新开关与间隔；修改链接保存后会立即
   重新拉取该订阅的节点。
2. **节点页** → 点测速：延迟缓存 5 分钟；不用的节点把开关关掉。
3. **配置页** → 校验 → 应用：`sing-box check`（或 mihomo `-t`）通过才会落盘重启；
   失败自动回滚上一份配置。
4. **透明代理页 / 局域网代理页**：按需开启。
5. **首页**确认「运行中」状态与流量曲线；面板里切内核（sing-box ↔ mihomo）一键完成，
   两边配置与快照互不覆盖。

#### A5. 日常运维

```bash
systemctl status sing-box-panel        # 面板服务状态（内核是面板子进程）
systemctl restart sing-box-panel       # 重启面板（连带重启内核）
journalctl -u sing-box-panel -f        # 面板自身日志
ss -tlnp | grep -E '9090|9091|7890'    # 端口确认
```

内核运行日志在面板「服务」页（环形缓冲不落盘）；连接与流量在「连接」页实时看。

#### A6. 升级版本

**自动更新（推荐）**：面板「设置 → 关于」会显示当前版本，有新版时点「更新到 vX.Y.Z」
即可——面板自动从本仓库 GitHub Releases 下载对应平台的包（直连失败自动走本机
mixed 入站重试，断点续传，sha256 校验不符即丢弃），校验通过后停核心 → 替换
panel 二进制（旧版备份为 `panel.exe.old` / `panel.old`，不动内核与 panel.conf）→
自动重启，页面稍后自动恢复。Windows 下运行中的面板同样支持原地更新，无需手动停服。

手动更新（自动更新不可用时）：

```bash
systemctl stop sing-box-panel
sudo install -m 755 新目录/panel /opt/sing-box-panel/bin/panel
systemctl start sing-box-panel
```

数据目录不动，订阅 / 节点 / 设置 / 快照全保留。

#### A7. 卸载

```bash
sudo systemctl disable --now sing-box-panel
sudo rm -f /etc/systemd/system/sing-box-panel.service /usr/local/bin/sing-box /usr/local/bin/mihomo
sudo rm -rf /opt/sing-box-panel
```

#### A8. 透明代理（本机 + 局域网 TCP 接管）

面板「透明代理」页打开总开关即可，nftables `inet singbox` 表由面板自动下发与撤除：

- 「接管网段」默认自动探测本机所在 RFC1918 网段，可手填；
- 「同时接管本机流量」= output 链也重定向（两种实现见下文「透明代理」章节的原理说明）；
- 「接管局域网 DNS」= 53 端口重定向到内核 DNS；
- 目标为内网的流量永远直连；redirect 只接管 TCP，QUIC 自动回落 TCP。

客户端（局域网设备）什么都不用装：**网关和 DNS 都指向本机 IP** 即生效。
细节、依赖与「本机接管」的两种实现见上文[透明代理](#透明代理)章节。

#### A9. 故障排查速查表

| 症状 | 原因与处理 |
| --- | --- |
| 启动报 `listen ...: Address in use` | 9090 被占：`ss -tlnp | grep 9090` 找到占用者，或改 panel.conf 的 listen |
| 面板开了但核心起不来 | 「服务」页看内核日志；「设置 → 内核」里能看到两个内核的探测结果 |
| 应用配置失败 | 内核 check 不过，报错原文会透出；先在节点页关掉可疑节点再应用 |
| 改完配置不生效 | 配置只在「应用」时生效；删订阅后也需要重新应用一次 |
| 订阅拉取报 403 | 机场按 UA 拦截：面板固定用 `clash-verge/v1.7.7` UA，一般不会；换个网络确认机场本身可达 |
| 订阅刷新成功但节点没变 | 机场返回 204（内容未变化/限流），面板按「未变化」处理属正常行为 |
| 测速全是 -1 | 所有节点超时：先确认首页出口 IP；或机场节点本身不可用 |
| **整网断网了** | 核心没跑但 redirect 规则还在（异常掉电等）：`nft delete table inet singbox` 立即恢复直连；正常关闭面板会自动撤表 |
| 面板密码忘了 | 见 A3「忘记密码」 |

### 场景 B：OpenWrt 路由器（MT7981 / AX3000 / CR880x 等，arm64）

#### B1. 前置检查

```bash
uname -m                      # 必须是 aarch64
df -h / /overlay              # 看可用空间；panel ~13MB + 内核（见 B2 空间方案）
```

面板与内核都是静态二进制，**不需要装任何依赖包**；只有 TUN 模式需要 `kmod-tun`。

#### B2. 下载解压

空间够就直接放路由器；不够就放 U 盘（挂载点下文以 `/mnt/usb` 为例）：

```bash
# 电脑上执行，Router_IP 换成路由器 LAN IP
scp sing-box-web-*-linux-arm64.tar.gz root@Router_IP:/tmp

# 路由器上执行
mkdir -p /opt/sing-box-panel
tar -xzf /tmp/sing-box-web-*-linux-arm64.tar.gz -C /opt/sing-box-panel --strip-components=1
cd /opt/sing-box-panel && ls
# panel  sing-box  mihomo  install.sh  panel.conf  sing-box-panel.service ...
```

> 16MB flash 机型：内核放不进 flash，改解压到 U 盘
> `mkdir -p /mnt/usb/singbox && tar -xzf ... -C /mnt/usb/singbox --strip-components=1`，
> 并确保 U 盘在开机阶段自动挂载（块设备挂载点 / extroot）。

#### B3. 首次启动（先手动跑通，再上自启）

```bash
cd /opt/sing-box-panel
./panel -data /opt/sing-box-panel/data -listen 0.0.0.0:9090 -password 你的密码
# 保持这个 SSH 会话不关，浏览器打开 http://Router_IP:9090 按 A4 流程配置；
# 配置应用成功后，Ctrl+C 停掉，进入 B4。
```

#### B4. 开机自启（procd）

面板里「设置 → 开机自启 → 面板」打开开关：检测到 OpenWrt（无 systemd）会自动生成
`/etc/init.d/sing-box-panel`（START=99，崩溃由 procd `respawn` 自动拉起）并 enable。
生成后可 `ls -l /etc/rc.d/S99sing-box-panel` 确认；改过安装路径就在面板里把开关关一次再开一次。

#### B5. 透明代理与客户端指向

面板「透明代理」页打开开关（接管网段会自动探测，多网段路由器按需手填）。
局域网设备**网关与 DNS 都指向路由器 LAN IP** 即被接管；路由器自己发出的流量默认不走代理。
依赖（nftables / iptables 两种实现）与本机接管的细节见上文[透明代理](#透明代理)章节。

#### B6. TUN 模式（可选）

```bash
opkg update && opkg install kmod-tun
```

然后面板里打开 TUN 开关（需要面板以 root 运行，OpenWrt 默认就是）。
TUN 与 redirect 可同时开：TUN 接管本机，redirect 接管局域网。

#### B7. OpenWrt 特别注意

- 防火墙（fw4）与面板的 nft 表（`inet singbox`）互不干扰；关掉面板开关会自动撤表防断网。
- 「同时接管本机流量」的 cgroupv2 方案按 systemd 的 cgroup 路径排除面板进程，
  OpenWrt（procd）下**不建议开启**，局域网接管不受影响（iptables 方案不受此限）。
- 整网断网的救援命令同 A9 表格（`nft delete table inet singbox` 或停面板服务）。

### 场景 C：MIPS 小路由（Cudy AC1200 / MT7621，16MB flash）

用 `*-linux-mipsle.tar.gz`（软浮点，`GOMIPS=softfloat` 纯静态编译）。
内存 128MB 够用：面板 ~13MB + 内核真实占用 ~25MB（RSS 里其余是内核可回收的代码页）。

**flash 是唯一硬约束**：16MB flash 刨掉系统通常只剩 1–2MB 可写，而内核解压后 15–20MB，
所以整体放外置存储：

```bash
# 插上 U 盘 / TF 卡后（OpenWrt 默认自动挂载到 /mnt/sda1 类路径）
mount | grep mmc\|sd         # 确认挂载点
mkdir -p /mnt/usb/singbox
tar -xzf /tmp/sing-box-web-*-linux-mipsle.tar.gz -C /mnt/usb/singbox --strip-components=1
cd /mnt/usb/singbox && ./panel -data /mnt/usb/singbox/data -listen 0.0.0.0:9090 -password 你的密码
```

其余流程与场景 B 完全一致（自启、透明代理、客户端指向）。
extroot（把 overlay 整体搬到 U 盘）的机器可以无视外置存储注意事项，直接当大 flash 机器用。

### 场景 D：Windows

详见上文 [Windows](#windows) 章节（解压 → 双击 `start.bat` → 浏览器自动打开面板；
TUN 需管理员；开机自启用面板内开关或计划任务）。

### 首次登录与安全

- 密码来源优先级：命令行 `-password` / `PANEL_PASSWORD`（unit 注释项） > `panel.conf` >
  全新面板未提供密码时进入网页首次初始化流程。
- `panel.conf` 的 `username` 与 `password` **都留空** = 关闭登录（打开端口直接进首页），
  只在内网可信环境使用；只留空 password = 保持现有密码。
- 面板自带登录限速（连续 5 次失败锁 30 秒，逐次翻倍至 15 分钟）与 Bearer token 会话（7 天）。
- 暴露公网：务必改强密码，或保持 `127.0.0.1` 监听 + 反向代理加鉴权。

## 界面

- **深浅色主题**：右上角月亮/太阳按钮切换，写入 `localStorage`，首次访问跟随系统 `prefers-color-scheme`。
  所有颜色都走语义 token（`--bg / --card / --field / --code-bg / --chart-down …`），没有硬编码色值。
- **排版**：正文 14px、表格 13px、辅助信息 11.5–12px，行高 1.56；深色主题的次要文字对比度从
  `#656c77` 提到 `#8b929d`，浅色主题用 `#414a57 / #667080`。
- 字体栈优先 `Inter → Segoe UI Variable Text → PingFang SC → Microsoft YaHei UI`，
  代码/地址用 `JetBrains Mono → ui-monospace`。
- **低配 GPU 优化**：弹窗遮罩不使用 `backdrop-filter`，顶栏为不透明底色，去掉全屏噪点混合层；
  弹窗只做 6px 位移淡入，阴影降为 `shadow-2`。系统开启「减少动态效果」时动画直接跳过。

### 订阅自动更新

每条订阅可以单独设置自动更新间隔（1–720 小时，默认 24 小时）：

- 订阅卡片上的开关控制该条订阅是否自动更新，旁边的「间隔 / 小时」输入框修改周期后点「保存」（或按回车）。
- 新增订阅时可同时设置开关与间隔；关闭自动更新时间隔输入框不可编辑。
- 调度器每分钟检查一次，到期才真正拉取，不会因为面板重启而额外更新。
- 旧版 `subs.json` 没有 `interval_hours` 字段，读取时按 24 小时处理，首次修改后写回。

## 透明代理

面板内「透明代理」页可以一键开关，默认 **redirect（仅 TCP）**：

1. 自动识别 RFC1918 网段（`192.168.1.0/24`、`10.0.0.0/24` 等）；
2. 下发 `table inet singbox`，`prerouting` 把来自这些网段的 TCP 重定向到 `:12345`；
3. 目标为内网（`10/8`、`172.16/12`、`192.168/16` 等）的流量直接 `return`；
4. 关闭开关即删表，流量恢复直连；规则文件落在 `data/tproxy.nft` 便于审计。

### 依赖：nftables 与「本机接管」的两种实现

透明代理依赖 `nft` 命令，**麒麟 V10 SP1 / 部分国产发行版默认没装**，需要先
`sudo apt install nftables`，否则开启时会直接报「找不到 nft 命令」。

「接管本机流量」要在 `output` 链里排除代理自己的出站，否则 sing-box 的连接会被重定向回自己形成回环。
面板按 nftables 版本自动选实现：

| nftables 版本 | 排除方式 | 说明 |
| --- | --- | --- |
| ≥ 1.0 | `socket cgroupv2 level N "<cgroup>"` | 精确匹配面板所在 cgroup，Debian 13 / Ubuntu 24.04 走这条 |
| 0.9.x（麒麟 V10 自带 0.9.3） | `meta mark 0x2023` | 核心启动时带 `route.default_mark`，自己给连接打标记，规则按标记放行 |

渲染配置时，root 下会自动写入 `route.default_mark: 8227`（非 root 跳过，因为设 `SO_MARK` 需要
`CAP_NET_ADMIN`）。如果配置里没有这个字段却要开「接管本机流量」，面板会拒绝并提示先重新渲染应用一次，
避免规则生效后回环断网。**局域网接管（`local=false`）不受此影响**，0.9.x 上也能正常用。

> 本机 DNS 入站默认监听 `127.0.0.1:53`，但麒麟自带 `dnsmasq` 占着这个端口。面板在渲染和应用配置前会
> 探测端口可用性，被占用时自动降级到 `5353 / 5335 / 15353 / 25353` 里第一个可用的，并在日志里说明；
> 全部不可用则摘掉该入站（核心照常工作，只是本机 DNS 接管不可用）。想让 `resolv.conf` 指过来时，
> 记得用日志里给出的实际端口。

还有一个 **「接管局域网 DNS」** 开关：在 `prerouting` 里把来自 LAN 网段的 53 端口（UDP/TCP）重定向到
`dns-lan`（`0.0.0.0:5354`），客户端即使把 DNS 指向主路由，也会拿到 sing-box 解析的干净结果。
代价是这些客户端不再使用路由器 dnsmasq 的本地域名。规则放在 `ip daddr @local_nets return` **之前**，
否则指向内网 DNS 的查询会被提前放行。

> **mihomo 内核下这套规则完全复用**：`12345` 对应 mihomo 的 redir 入站，`5354` 对应
> `dns.listen: 0.0.0.0:5354`，都由面板渲染配置时自动写入（只在 Linux 上写，其它平台会删掉，
> 否则 mihomo 启动失败）。所以切内核不需要重下 nft 规则；面板会重新核对一遍端口是否对得上，
> 对不上就自动关掉透明代理并提示，避免「规则还在但没人监听」导致整网断掉。
>
> ⚠️ mihomo 的 redir 入站**不能写成顶层 `redir-port`**。`redir-port` 的监听地址受
> `bind-address` / `allow-lan` 支配，默认的 `bind-address: 127.0.0.1` + `allow-lan: false`
> 会把它绑到 `127.0.0.1:12345`；而 nft 的 redirect 目标是**入接口地址**，局域网客户端连的是
> `192.168.x.y:12345`，那里没人监听 —— 现象是 nft 规则命中却 `Connection refused`，
> 看着像「透明代理开了但没用」。删掉 `bind-address` 也没用，`allow-lan: false` 一样锁回环。
> 所以渲染层改用 `listeners` 显式声明：
>
> ```yaml
> listeners:
>   - name: redir-in
>     type: redir
>     port: 12345
>     listen: 0.0.0.0
>     udp: false
> ```
>
> 这样 `mixed-port` 仍然锁在 `127.0.0.1`（系统代理入口不需要对外开放），只有 redir 入站对外。
> 面板在开启透明代理时会检查这一点：如果入站只绑了回环地址，会明确告警「局域网设备会被拒绝连接」。

另外有一个独立开关 **「同时接管本机流量」**：在 `output` 钩子上做同样的 redirect，
让服务器自己发起的 TCP 也走代理（`curl ifconfig.me` 会返回代理出口）。
它会先 `socket cgroupv2 return` 排除面板与 sing-box 自身的 cgroup，避免流量回环；
无法识别 cgroup 时会直接拒绝开启。

> 前提：客户端的网关要指向本机。旁路由场景下本机默认路由和 LAN 在同一张网卡也能正常工作。

### 局域网代理

透明代理页有独立的 **「局域网代理」** 卡片：SOCKS5 与 HTTP 各占一个端口。

- 默认 `1080` / `8118`，可在页面改（1024–65535，且不能与面板/核心端口冲突）
- 用户名与密码都填写 = 鉴权模式，密码至少 8 位；页面提供「生成」按钮生成 16 位随机密码
- 用户名与密码都留空 = **无鉴权开放代理**，页面会二次确认；只填一个会报错
- 开启后会重新渲染配置并重启核心（约 1 秒断流）
- `mixed` 入站已收回 `127.0.0.1:7890`，不再对局域网开放无鉴权代理

```bash
curl -x socks5h://user:pass@192.168.1.10:1080 https://ifconfig.me
curl -x http://user:pass@192.168.1.10:8118   https://ifconfig.me
```

### 路由模式（全局代理 / 分流）

「代理模式（Windows）/ 透明代理（Linux）」页的「路由模式」卡片对 TUN、透明代理、局域网代理和系统代理同时生效：

| 模式 | 行为 | DNS |
| --- | --- | --- |
| 规则分流（默认） | 国内域名/IP 直连，其余走节点，按模板里的 geosite/geoip 规则 | 国内解析走本地，其余走 DoH（经代理） |
| 全局代理 | **包括国内流量在内全部走节点**；仅保留内网直连（私有地址无法经节点转发） | 全部走 DoH（经代理） |
| 全局直连 | 全部直连，不经过节点；用于排查节点或路由问题 | 全部走本地 DNS |

切换模式会重新渲染配置并重启核心（约 1 秒断流），`sing-box check` 或探活失败会自动回滚到上一份配置与上一个模式。
注意全局代理下国内支付、视频等服务可能因境外出口 IP 变慢或受限；全局直连时节点仍保持连接但不再承载流量。

### 分流与 DNS

- 规则分流模式的路由顺序：`sniff → hijack-dns → 私有地址 direct → geosite-cn direct → geoip-cn direct → final proxy`。
  国内站点直连（实测 baidu 约 0.07s），国外走代理。
- 规则集用 `remote + binary`，缓存在 `data/cache.db`，7 天更新一次。
- **规则集下载走 jsDelivr 镜像 + 独立直连出站**，这是两处必须的坑：
  - `raw.githubusercontent.com` 在境内实测完全不可达（10 秒零字节），默认模板如果指向它，
    开箱即用必然失败：核心 `FATAL initialize rule-set` → 崩溃循环 → 用户连面板都进不去排查。
    所以模板改用 `cdn.jsdelivr.net`（实测 1.4 秒可下）。老数据里已经存在的模板会被自动迁移：
    只精准替换两个内置默认 URL，用户自己改过的值不动，幂等。
  - 规则集下载**不能挂在节点出站上**（`detour: proxy`），否则「节点全挂 → 下不到规则集 →
    核心起不来」形成死锁。但也不能 `detour: direct`：sing-box 会报
    `detour to an empty direct outbound makes no sense`。面板为此单独渲染了一个
    `{"type":"direct","tag":"rule-set","tcp_fast_open":true}` 出站，只给规则集下载用，
    不进 selector，用户选不到。
- 本机 DNS：`dns-in` 入站监听 `127.0.0.1:53`，`/etc/resolv.conf.head` 写入 `nameserver 127.0.0.1`，
  dhcpcd 重新生成 resolv.conf 时仍会把它放在首位；国内域名走 223.5.5.5，其余走 DoH（经代理）。
- **必须**配合 `{"protocol":"dns","action":"hijack-dns"}` 规则：`direct` 入站本身只是把 DNS 查询当普通连接，
  缺这条规则会命中“私有地址直连”而自己连自己，形成死循环。

## 接口一览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/auth/login` | 口令换 Bearer Token（关闭登录时返回 `auth_disabled`） |
| GET | `/api/v1/auth/status` | 公开接口：`required` 表示是否启用登录，前端据此跳过登录页 |
| PUT | `/api/v1/auth/access` | 面板内登录开关：`{"disabled":true}` 关闭；启用时需带 `username` 与至少 8 位 `password`，旧 Token 全部失效 |
| GET | `/api/v1/status` | 核心状态、RSS、配置版本、当前出站；含 `listen`、`listen_pinned`、`engine`、`engine_label`、`engine_bin` |
| GET/PUT | `/api/v1/engine` | 内核：GET 返回两个内核的可用性 / 版本 / 错误原因；PUT `{"engine":"mihomo"}` 切换（失败自动回退） |
| GET | `/api/v1/geodata` | mihomo 的 geo 数据状态：`idle` / `downloading` / `ready` / `failed`，含 `missing`、`embedded` |
| POST | `/api/v1/geodata/prefetch` | 手动触发后台预取，立即返回，进度看上面那个接口 |
| PUT | `/api/v1/settings/listen` | 热切换监听地址并回写 `panel.conf`：`{"listen":"0.0.0.0:9090"}`；启动参数 `-listen` 固定时返回 `warning` |
| GET/POST | `/api/v1/subscriptions` | 订阅列表 / 新增（立即拉取解析，可带 `auto`、`interval_hours`） |
| PUT | `/api/v1/subscriptions/{id}` | 修改自动更新开关 / 间隔（1–720 小时） |
| POST | `/api/v1/subscriptions/{id}/refresh` | 重新拉取 |
| GET | `/api/v1/nodes` | 节点 + 内存中的延迟缓存 |
| POST | `/api/v1/nodes/delay` | 按设置的并发数（默认 3，上限 16）测速，TTL 300s || GET | `/api/v1/config` | 当前配置 + 快照列表 |
| POST | `/api/v1/config/render` | 模板 + 节点合并 |
| POST | `/api/v1/config/validate` | `sing-box check` / `mihomo -t`（按当前内核） |
| POST | `/api/v1/config/apply` | check → 快照 → 原子写 → 重启 → 探活 → 失败回滚 |
| POST | `/api/v1/config/rollback` | 回滚到指定快照 |
| POST | `/api/v1/service/{start\|stop\|restart}` | 进程控制 |
| GET/PUT | `/api/v1/autostart` | 开机自启：`panel`=系统级自启（Linux systemd / Windows 计划任务），`core`=面板启动时自动拉起核心 |
| GET/PUT | `/api/v1/tun` | TUN 模式状态 / 开关（Windows 全局接管，需管理员） |
| GET/PUT | `/api/v1/sysproxy` | Windows 系统代理：`{"enabled":bool}` 开/关（指向 mixed 端口，还原原值，广播刷新）；GET 返回 `{enabled, server, supported}` |
| GET/PUT | `/api/v1/routing` | 路由模式：`rule`（国内直连）/ `global`（全部走节点）/ `direct`（全部直连） |
| WS | `/api/v1/ws/connections` | 连接流，服务端裁剪 + 2s 节流 + 200 条上限（前端另有 REST 兜底） |
| WS | `/api/v1/ws/traffic` | 上下行速率，2s 一帧 |

> **延迟数字不要跨内核比较。** 面板的延迟来自内核自带的 Clash API
> `GET /proxies/{name}/delay`，两个内核的测量口径不同：sing-box 每次新建连接并跑完整
> TLS/reality 握手，mihomo 复用连接池。同一节点、同一时刻实测 sing-box 579 ms、
> mihomo 145 ms，而两者真实 TTFB 都是 200 ms 左右。切换内核时面板会清空延迟缓存
> （`ResetDelays`），避免旧内核的数值继续显示造成误判。

## 实测结果

| 项目 | 数值 |
| --- | --- |
| 订阅 | 2 个，共解析 56 个节点（自动过滤「剩余流量/套餐到期」等信息占位条目） |
| 配置 | 1306 行 / 36 KB，`sing-box check` 通过（约 100 ms） |
| 核心 | sing-box 1.14.3，RSS ~72 MB |
| 面板 | RSS **16 MB**（空闲）/ **22 MB**（浏览器开着、含 geo 内置副本）；二进制 15.8 MB |
| 代理 | `curl -x http://127.0.0.1:7890 https://www.gstatic.com/generate_204` → 204 |
| 连接 | 峰值 32 条，前端渲染 200 条上限，2s 节流 |
| 透明代理 | **已启用**（redirect），接管 3 个网段；netns 模拟客户端访问 YouTube 返回 200 |
| mihomo 内核 | v1.19.33，配置 106 行 YAML，`mihomo -t` 0.27 s（geo 已预置） |
| 内核切换 | 双向切换均成功：sing-box **0.77 s**、mihomo **1.15 s**（rule-set 缓存命中后） |
| geo 预取 | 面板启动后台下 12 MB：本机 3 s、Debian 虚拟机 3 s（并行 + 慢速换源） |

## 内存去哪了

面板常驻 16–22 MB，**Go 堆里只有 1–2 MB**（`GODEBUG=gctrace=1` 实测，每次 GC 后
`heap_alloc` 都是 1~2 MB）。剩下的按块拆：

| 部分 | 量级 | 说明 |
| --- | --- | --- |
| Go 运行时基线 | ~8 MB | 线程栈、GC 元数据、每 P 的分配缓存；空服务也是这个数 |
| 可执行映像 | ~7 MB | 二进制里被真正执行到的代码页，只读、可被系统回收 |
| 堆高水位 | 0~5 MB | 浏览器开着时每 3 s 拉 400 行日志、两个 WebSocket 推送，堆会冲高，GC 后不还 |
| geo 内置副本 | 0~5 MB | 只在二进制里打包了 geo 时才占，且仅在被读过之后 |

三个已经落地的优化：

1. **geo 内置副本改成 gzip**：12 MB → 5 MB，二进制 22.5 MB → 15.8 MB，
   解压后进 RSS 的也只有 5 MB 而不是 12 MB。
2. **解压走流式**：以前 `embed.ReadFile` 会把整份 7.6 MB 读进堆再写盘（峰值多 7.6 MB），
   现在 `io.Copy` + 32 KB 缓冲，堆占用可以忽略。
3. **空闲回收**：90 s 没有 API 请求就 `debug.FreeOSMemory()` 把空闲页还给系统。
   实测（对照程序）：分配 40 MB → GC 后 `Sys` 仍是 46.9 MB、RSS 49.9 MB 不动；
   `FreeOSMemory` 后 `released` 43.3 MB、RSS 掉回 9.7 MB。**普通 GC 不还内存，只有它才还。**

还可以再压的地方（都有代价，没做）：

- `-ldflags "-s -w"`：二进制 15.8 → 11 MB，磁盘占用变小，RSS 几乎不变（符号表本来也不常驻）。
- 不打包 geo：RSS 少 5 MB，代价是首次切 mihomo 要自己下 12 MB（3~30 s）。
- 日志增量拉取（`?since=<seq>`）：现在每 3 s 全量拉 400 行，只影响 GC 频率，不影响峰值。
- `GOMAXPROCS` 限制到 2~4：每 P 的分配缓存会少一点，但面板本来也不吃 CPU。
| 主题 | 深/浅双主题，图表配色跟随 CSS 变量重绘 |

### Debian 13 真机双内核实测（sing-box 1.14.3 / mihomo v1.19.33）

在 Debian 13（内核 6.12，nft 1.1.3，root）上做的端到端验证，用 `ip netns` + veth 模拟真实
局域网客户端（`10.99.0.2`，网关指向本机），两个内核分别跑通：

| 项目 | sing-box | mihomo |
| --- | --- | --- |
| 渲染 + 校验 + 应用 | 231 行 JSON，`check` 通过，apply 0.7 s | 配置含 `listeners`/`proxies`/`proxy-groups`，`-t` 通过 |
| mixed 系统代理 | `127.0.0.1:7890` → 204 | `127.0.0.1:7890` → 204 |
| 透明代理入站 | `0.0.0.0:12345`（redirect） | `0.0.0.0:12345`（`listeners` 的 redir） |
| netns 客户端 HTTPS | 204（0.16 s） | 204（0.16 s） |
| netns 客户端 DNS 劫持 | 生效（`0.0.0.0:5354`） | 生效（`0.0.0.0:5354`） |
| 连接表证据 | `"type":"redirect/redirect-in"`, `sourceIP 10.99.0.2` | 日志 `[TCP] 10.99.0.2:xxxx --> www.gstatic.com:443 match GeoIP(cn) using DIRECT` |
| 走节点路径 | `rule_set=geosite-cn => route(direct)` | `[TCP] 10.99.0.2:xxxx --> 1.1.1.1:443 match Match using proxy[本地SS-真实可用]` |

这一轮实测发现并修掉的 6 个真实缺陷（都不是「编译能过」能暴露的）：

1. `ss://` 的 scheme 被直接当成 outbound type，渲染出 `type: ss` → `sing-box check` 报
   `unknown outbound type: ss`，apply 直接 400。修法：`model.NormalizeOutboundType()`，
   解析时归一 + 渲染时再归一一次（兼容库里已存的老数据）。
2. 默认模板的规则集指向 `raw.githubusercontent.com`，境内不可达 → 核心 FATAL 崩溃循环。
   修法：换 jsDelivr + 对已有模板做幂等迁移（见「分流与 DNS」）。
3. 规则集 `detour` 指向空 `direct` 被 sing-box 拒绝 → 新增独立的 `rule-set` 直连出站。
4. `Preflight` 只看 `/sys/module/nf_nat_redirect` 目录，而 Debian 内核是
   `CONFIG_NF_NAT_REDIRECT=y`（内建，不生成目录）→ 明明能用却报「内核缺少 NAT redirect 支持」。
   修法：目录不命中时用 `nft -c -f -` 真解析一遍探测。
5. mihomo 的 `redir-port` 被 `bind-address`/`allow-lan` 绑到回环，LAN 透明代理
   `Connection refused`（详见「透明代理」一节）。
6. `SweepOrphans` 只按可执行文件路径清扫，会误杀用户手动起的同款二进制
   （例如拿同一份 `sing-box` 跑个测试服务端）。修法：再加一条「命令行里必须引用本面板
   数据目录」，读不到命令行时仍按孤儿处理（宁可多杀也不能漏，漏掉的孤儿会让新核心起不来）。

## 从 v2rayA 迁移 / 回滚

接管前建议先备份原工具的配置与规则（例如 `/root/pre-singbox-backup/` 下的
`etc-v2raya.tgz`、`nft.ruleset`、`enabled.txt`）。

```bash
# 回滚：停面板（会连带停掉 sing-box 并撤销 nftables 表），再启回原工具
systemctl disable --now sing-box-panel
pkill -x sing-box
systemctl start v2raya v2raya-switcher
```

v2rayA 启动时会自行恢复 nftables 表与透明代理规则。

## 设计说明

- **内核差异收敛在一个包**：`internal/service/engine` 只回答「当前内核是谁」和
  「配置叫什么名字、怎么启动、怎么校验、工作目录在哪」，上层（store / supervisor /
  config / api）不再出现 `if mihomo` 散落各处的分支。加第三个内核只需在这个包里补一个分支。
- **节点存储与内核解耦**：库里存的始终是 sing-box outbound 片段，mihomo 渲染时才翻译。
  好处是一份订阅解析结果给两个内核复用，测速/选节点/连接页都不用分叉；
  代价是翻译层要覆盖协议差异（TLS 的 `sni` vs `servername`、`reality-opts`、
  `ws` / `grpc` / `http(h2)` 传输），不支持的协议会被跳过而不是写坏配置。
- UI 采用 shadcn/ui 的视觉语言（token、圆角、边框、badge、switch、dialog、命令面板），
  但**没有引入任何组件库**，全部原语为手写 CSS，约 48 KB。
- 前端为无框架运行时（原生 JS），与「路由器上不跑 Node、不引入运行时 DOM 框架」的约束一致。
  所有与内核相关的文案（配置文件名、语言、校验命令、TUN 网卡名）都从 `status.engine` 派生，
  页面里不写死 sing-box。
- 延迟、连接、日志全部只在内存：面板重启即清零，不写磁盘。
- 订阅链接由已登录管理员填写，允许指向内网地址（自建订阅源是合法场景）；面板不按 SSRF
  拦截私有网段。请只把管理端口暴露在可信网络，或放在带鉴权的反向代理之后。
