# sing-box panel

面向低配设备（MT7981 / 256 MB）的 sing-box Web 控制面：**单二进制、前端 embed、常驻内存 ~11 MB**。
已在一台 Debian 13 x86_64 服务器上用两个真实机场订阅完成端到端验证。

## 下载即用（推荐）

到 [Releases](https://github.com/jiqing112/sing-box-web/releases) 下载对应平台的包，
里面**已经带好了对应平台的 sing-box 内核**，解压就能跑，不需要装 Go、不需要自己找内核。

| 平台 | 文件 | 用法 |
| --- | --- | --- |
| Windows x64 | `sing-box-web-*-windows-amd64.zip` | 解压 → 双击 `start.bat` → 浏览器自动打开面板 |
| Linux x64 | `sing-box-web-*-linux-amd64.tar.gz` | 解压 → `sudo ./install.sh` |
| Linux arm64 | `sing-box-web-*-linux-arm64.tar.gz` | 同上（ARM 路由器 / 小主机） |

- 每个包都附 `.sha256` 校验文件。
- 首次启动会生成随机密码，Windows 在 `panel-data\initial-password`，
  Linux 在 `/opt/sing-box-panel/data/initial-password`（安装脚本会直接打印出来）。
- 包里的 sing-box 版本随发布固定（当前 **1.14.3**），面板会自动在同目录 / `/usr/local/bin` 找到它。
- 发新版时 GitHub Actions 会自动重新下载对应平台的内核并打包，见 `.github/workflows/release.yml`。

## 仓库结构

| 路径 | 内容 |
| --- | --- |
| `app/` | **可编译的成品**：Go 后端 + embed 前端 + systemd 单元 |
| `app/deploy/` | 发布包用的部署文件：`panel.conf` 模板、`install.sh`、`start.bat`、systemd 单元 |
| `.github/workflows/release.yml` | 打 tag 自动编译三平台 + 下载对应 sing-box 内核 + 打包发布 |
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
./panel -data ./data -listen 127.0.0.1:9090 -password your-pass
```

首次启动会生成 `data/settings.json`、`data/template.json`；`data/config.json` 要等你第一次在「配置」页
保存应用（或触发一次需要重渲染的开关）后才会写入。

下文出现的 `data/` 一律指 **`-data` 指定的数据目录**，不是系统根目录的 `/data`：
Linux 默认 `/opt/sing-box-panel/data`，Windows 默认 exe 同级的 `panel-data\`。

## Windows

```powershell
cd app
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 `
  go build -trimpath -ldflags="-s -w" -o ..\panel-windows-amd64.exe ./cmd/panel
```

- 不带 `-data` 时，数据目录默认是 exe 旁边的 `panel-data\`。

### sing-box.exe 的三种部署方式

面板按以下顺序定位 sing-box，任选其一：

1. **同目录（推荐）**：把官方 `sing-box.exe` 放在面板 exe 旁边，零配置自动发现；
2. **加入 PATH**：面板找不到同目录的 `sing-box.exe` 时，回退为按 `PATH` 解析 `sing-box.exe`；
3. **启动参数显式指定**：`panel-windows-amd64.exe -sing-box "D:\tools\sing-box\sing-box.exe"`。

方式 3 需要同步修改计划任务/快捷方式的启动参数；方式 2 需要确认面板进程的 PATH 环境
（计划任务以 SYSTEM 运行时，用的是系统 PATH 而不是用户 PATH）。

- **「透明代理」菜单双平台复用**：Linux 显示 nftables redirect 接管（含局域网 DNS/本机
  接管开关、nftables 规则预览）；Windows 同一菜单显示为「TUN 模式」（虚拟网卡全局接管），
  两边的「局域网代理」设置都可在这页管理。tproxy API 在 Windows 上仍返回明确的
  unsupported 错误；默认模板也不会生成 sing-box 在 Windows 上无法运行的 `redirect` /
  53 端口 DNS 入站（避免核心崩溃循环）。
- **Windows 全局接管用 TUN 模式**：服务页卡片与「TUN 模式」页一键开关（`GET/PUT /api/v1/tun`）。
  面板必须以**管理员身份**运行（子进程继承令牌才能创建 wintun 虚拟网卡 `singbox-tun`）；
  开启后整机流量（包括不走系统代理的程序）经 `auto_route` 进入 sing-box。开关会重新渲染
  配置并重启核心，失败自动回滚。
- **路由模式**：「TUN 模式 / 透明代理」页顶部提供三档——规则分流（国内直连、其余走节点）、
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

## 部署（Debian / Ubuntu）

发布包里自带 `install.sh`，一条命令装好（面板 + 内核 + systemd 服务 + 开机自启）：

```bash
tar -xzf sing-box-web-*-linux-amd64.tar.gz
cd sing-box-web-*-linux-amd64
sudo ./install.sh
```

手动部署的话：

```bash
mkdir -p /opt/sing-box-panel/{bin,data}
cp panel /opt/sing-box-panel/bin/
install -m755 sing-box /usr/local/bin/sing-box
cp app/deploy/sing-box-panel.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now sing-box-panel
```

面板启动时会自己拉起 `sing-box` 子进程；`KillMode=control-group` 保证停止时整组退出。

端口/账户/密码同样可用 exe 旁边的 `panel.conf` 管理（见 Windows 一节的说明）；unit 里显式
传的 `-listen`、`PANEL_PASSWORD` 优先于配置文件。由于本 unit 固定了
`-listen 0.0.0.0:9090`，设置页的监听地址会显示固定提示：热切换只对当前进程生效，重启后
仍回到 unit 中的地址；永久变更请改 unit 后执行 `systemctl daemon-reload && systemctl restart sing-box-panel`。

unit 里的 `PANEL_TOKEN` / `PANEL_PASSWORD` 默认注释掉：不设置时面板首次启动会生成随机口令，
写入**数据目录**（`-data` 指定的那个目录，默认 `/opt/sing-box-panel/data`）下的 `initial-password`，
避免把弱口令带上线。注意是数据目录里的 `data/`，不是系统根目录的 `/data`。

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

还有一个 **「接管局域网 DNS」** 开关：在 `prerouting` 里把来自 LAN 网段的 53 端口（UDP/TCP）重定向到
`dns-lan`（`0.0.0.0:5354`），客户端即使把 DNS 指向主路由，也会拿到 sing-box 解析的干净结果。
代价是这些客户端不再使用路由器 dnsmasq 的本地域名。规则放在 `ip daddr @local_nets return` **之前**，
否则指向内网 DNS 的查询会被提前放行。

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

「透明代理 / TUN 模式」页顶部的「路由模式」卡片对 TUN、透明代理、局域网代理和系统代理同时生效：

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
- 规则集用 `remote + binary`，经代理下载，缓存在 `data/cache.db`，7 天更新一次。
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
| GET | `/api/v1/status` | 核心状态、RSS、配置版本、当前出站；含 `listen`、`listen_pinned` |
| PUT | `/api/v1/settings/listen` | 热切换监听地址并回写 `panel.conf`：`{"listen":"0.0.0.0:9090"}`；启动参数 `-listen` 固定时返回 `warning` |
| GET/POST | `/api/v1/subscriptions` | 订阅列表 / 新增（立即拉取解析，可带 `auto`、`interval_hours`） |
| PUT | `/api/v1/subscriptions/{id}` | 修改自动更新开关 / 间隔（1–720 小时） |
| POST | `/api/v1/subscriptions/{id}/refresh` | 重新拉取 |
| GET | `/api/v1/nodes` | 节点 + 内存中的延迟缓存 |
| POST | `/api/v1/nodes/delay` | 按设置的并发数（默认 3，上限 16）测速，TTL 300s |
| GET | `/api/v1/config` | 当前配置 + 快照列表 |
| POST | `/api/v1/config/render` | 模板 + 节点合并 |
| POST | `/api/v1/config/validate` | `sing-box check` |
| POST | `/api/v1/config/apply` | check → 快照 → 原子写 → 重启 → 探活 → 失败回滚 |
| POST | `/api/v1/config/rollback` | 回滚到指定快照 |
| POST | `/api/v1/service/{start\|stop\|restart}` | 进程控制 |
| GET/PUT | `/api/v1/autostart` | 开机自启：`panel`=系统级自启（Linux systemd / Windows 计划任务），`core`=面板启动时自动拉起核心 |
| GET/PUT | `/api/v1/tun` | TUN 模式状态 / 开关（Windows 全局接管，需管理员） |
| GET/PUT | `/api/v1/routing` | 路由模式：`rule`（国内直连）/ `global`（全部走节点）/ `direct`（全部直连） |
| WS | `/api/v1/ws/connections` | 连接流，服务端裁剪 + 2s 节流 + 200 条上限（前端另有 REST 兜底） |
| WS | `/api/v1/ws/traffic` | 上下行速率，2s 一帧 |

## 实测结果

| 项目 | 数值 |
| --- | --- |
| 订阅 | 2 个，共解析 56 个节点（自动过滤「剩余流量/套餐到期」等信息占位条目） |
| 配置 | 1306 行 / 36 KB，`sing-box check` 通过（约 100 ms） |
| 核心 | sing-box 1.14.3，RSS ~72 MB |
| 面板 | RSS **10.9–13.5 MB**，二进制 6.7 MB |
| 代理 | `curl -x http://127.0.0.1:7890 https://www.gstatic.com/generate_204` → 204 |
| 连接 | 峰值 32 条，前端渲染 200 条上限，2s 节流 |
| 透明代理 | **已启用**（redirect），接管 3 个网段；netns 模拟客户端访问 YouTube 返回 200 |
| 主题 | 深/浅双主题，图表配色跟随 CSS 变量重绘 |

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

- UI 采用 shadcn/ui 的视觉语言（token、圆角、边框、badge、switch、dialog、命令面板），
  但**没有引入任何组件库**，全部原语为手写 CSS，约 48 KB。
- 前端为无框架运行时（原生 JS），与「路由器上不跑 Node、不引入运行时 DOM 框架」的约束一致。
- 延迟、连接、日志全部只在内存：面板重启即清零，不写磁盘。
- 订阅链接由已登录管理员填写，允许指向内网地址（自建订阅源是合法场景）；面板不按 SSRF
  拦截私有网段。请只把管理端口暴露在可信网络，或放在带鉴权的反向代理之后。
