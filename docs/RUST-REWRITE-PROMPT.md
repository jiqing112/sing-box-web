# sing-box 面板 · Rust 重写任务书

> 用法：把本文件**整份**贴给 AI，作为唯一需求来源。文件里带「实测」字样的条目都是上一版
> （Go 实现）踩过的真实坑，不要凭直觉改掉，否则会重现同样的故障。

---

## 0. 一句话任务

把一个已经用 Go 写好的、能跑的双内核代理面板，用 Rust **等价重写**：
功能不减少、数据格式不变、前端不改，产物体积和常驻内存显著更小。

---

## 1. 背景

现有实现（Go 1.22）是一个**单二进制、前端 embed** 的代理面板，同时驱动两个内核：

- `sing-box` 1.14.x：JSON 配置，`run -c <file>`，校验 `check -c <file>`
- `mihomo` v1.19.x：Clash YAML 配置，`-d <home> -f <file>`，校验 `-t -d <home> -f <file>`

两个内核共用一套 Clash API（`127.0.0.1:9091`）供面板读状态、测延迟、拉连接、推流量。

现有 Go 版实测（Windows / Linux）：

| 指标 | Go 版 |
| --- | --- |
| 二进制 | 15.8 MB（含 5 MB gzip 后的 geo 数据） |
| 常驻内存（Windows，浏览器开着） | 22–25 MB |
| 常驻内存（Linux，空闲） | 12–16 MB |
| 非测试代码 | 7,703 行 / 45 文件 / 2 个第三方依赖 |

Rust 版的目标是**把二进制压到 6 MB 以内、常驻内存压到 Linux 8 MB / Windows 12 MB 以内**，
同时保持所有行为一致。这不是"重新设计一个更好的面板"，是"用更省资源的方式重做同一个"。

---

## 2. 不可协商的约束

1. **前端完全复用**：`web/dist/{index.html,app.js,styles.css}` 原样 embed，一个字符都不改。
   所有 HTTP 路径、请求体、响应字段必须与现有 API 完全一致，否则前端要跟着改。
2. **数据目录格式不变**：用户已有的 `panel-data/` 目录必须能被新二进制直接打开，
   不需要迁移脚本，不丢订阅、不丢设置、不丢快照。
3. **双内核都要支持**：不能在重写时砍掉 mihomo。
4. **跨平台**：Windows（主要）、Linux（服务器/OpenWrt）、macOS（至少能编译能跑）。
5. **单二进制**：前端 + 默认模板 + geo 数据全部编进可执行文件，运行时不需要任何附属文件。
6. **不引入 C/C++ 依赖链**：优先纯 Rust crate，避免 openssl/curl 这类需要本地工具链的东西。

---

## 3. 产物矩阵

| 目标 | triple |
| --- | --- |
| Windows x64 | `x86_64-pc-windows-msvc`（或 `-gnu`） |
| Linux x64 | `x86_64-unknown-linux-musl`（静态，OpenWrt 必需） |
| Linux arm64 | `aarch64-unknown-linux-musl` |
| Linux armv7 | `armv7-unknown-linux-musleabihf`（老路由器） |
| macOS x64 / arm64 | `x86_64-apple-darwin` / `aarch64-apple-darwin` |

要求：一条 `make release` 能出全部产物；交叉编译用 `cargo-zigbuild`（Linux）+
`osxcross`（macOS）解决，在 CI 里配好。

---

## 4. 推荐技术选型

```toml
[dependencies]
tokio = { version = "1", features = ["rt-multi-thread", "net", "time", "process", "sync", "io-util", "macros"] }
axum = "0.8"                  # HTTP + 路由
tower-http = { version = "0.6", features = ["fs", "compression"] }   # 静态文件 + gzip
serde = { version = "1", features = ["derive"] }
serde_json = "1"
serde_yaml = "0.9"            # mihomo 配置 + Clash 订阅解析
tokio-tungstenite = "0.24"    # 面板 → 浏览器的 WS；同时作为客户端连 Clash API
reqwest = { version = "0.12", default-features = false, features = ["rustls-tls", "json", "stream"] }
sha2 = "0.10"                 # 密码哈希、Token
rand = "0.8"
base64 = "0.22"
flate2 = "1"                  # geo gzip 解压
regex = "1"                   # 日志 ANSI 转义剥离
percent-encoding = "2"        # v2ray URI 解析
[target.'cfg(windows)'.dependencies]
windows-sys = { version = "0.59", features = ["Win32_Foundation", "Win32_System_ProcessStatus", "Win32_System_JobObjects", "Win32_System_Threading", "Win32_System_Registry"] }
[target.'cfg(unix)'.dependencies]
nix = { version = "0.29", features = ["signal", "process"] }
libc = "0.2"
```

选型理由（写进代码注释里）：
- **不要用 rustls 之外的 TLS**：musl 静态链接时 openssl 会拖进一堆系统库。
- **不要用 `actix-web`**：它自己起一套 runtime，内存比 axum 高。
- **`tokio` 用 multi-thread**：面板要同时跑 HTTP、WS、子进程 pump、定时器，
  单线程会让"切内核时接口假死"。若确实要省内存，可退到 `current_thread` +
  显式 `spawn_blocking` 处理文件 IO，但要实测接口 P99。

---

## 5. 目录结构

```
src/
  main.rs                 入口：参数解析、启动顺序
  cli.rs                  -data/-listen/... 与 panel.conf 解析
  config/
    mod.rs                设置结构体 Settings
    render.rs             模板 → 配置（sing-box / mihomo 两套）
    migrate.rs            模板迁移（见 §9.3）
    default_tpl.rs        内置默认模板（include_str!）
  store/
    mod.rs                数据目录读写、原子写、快照
  engine/
    mod.rs                内核抽象：文件名、命令行、home 目录
  supervisor/
    mod.rs                子进程生命周期、日志环形、崩溃计数
    win.rs                Job Object 杀进程树
    unix.rs               setsid + 进程组 kill
  clash/
    mod.rs                Clash API 客户端：version/proxies/delay/connections/traffic
    delay.rs              延迟缓存（TTL + 并发限制）
  subscription/
    mod.rs                拉取 + 自动识别三种格式
    clash_yaml.rs         Clash YAML → sing-box outbound
    v2ray_uri.rs          ss/vmess/vless/trojan/hysteria2 URI → outbound
  geodata/
    mod.rs                三层兜底 + 并行下载 + 慢速换源
    assets/               （可选）GeoSite.dat.gz / geoip.metadb.gz
  tproxy/
    mod.rs                nftables 生成与应用（Linux only）
    detect.rs             网段探测
  api/
    mod.rs                axum Router、中间件
    auth.rs               登录、Token、限流
    handlers/             按资源拆：status/logs/subs/nodes/config/engine/…
    ws.rs                 两个 WebSocket
    autostart/            windows.rs / unix.rs / macos.rs
  web/
    mod.rs                embed 静态资源 + ETag
tests/
  ...                     见 §12
```

---

## 6. 启动顺序（顺序错了会出事）

严格按这个顺序，每一步都有原因：

1. 解析 CLI 参数 → 读 `panel.conf` → 合并（**命令行优先**）。
2. 规范化 `data_dir` 为绝对路径（后面所有路径都基于它，相对路径在 Windows
   服务/计划任务下会指向 `C:\Windows\System32`）。
3. 打开 store：读 `settings.json`，缺字段补默认值，`engine` 空值按 `sing-box` 处理。
4. **清理孤儿内核进程**：上一版被强杀时，子内核会活下来占着 9091 和 cache.db。
   必须在探活和自动启动之前清掉，否则会被"Clash API 可用"误判成核心健在。
   清理时必须**限定同一数据目录**（见 §11.4）。
5. TUN 降级检查（Windows）：非管理员启动时若 `tun_enabled=true`，
   自动改渲染成无 TUN 配置并写回设置，日志提示"以管理员重启后可再开启"。
6. 构造 Clash 客户端、tproxy 管理器、HTTP 服务。
7. 自动启动核心（如果开了）：先探活 1.5 s，已在跑就跳过（避免端口冲突）。
8. **后台预取 geo 数据**（mihomo 用，约 12 MB）。放在核心启动之后，别抢启动带宽。
9. **恢复透明代理**（仅当核心确认运行 + 配置里真有 redirect 入站）。
   顺序不能反：核心没起来就下发 nft 规则 = 流量被重定向到没人监听的端口 = 整机断网。
10. 起订阅自动更新调度器、空闲内存回收定时器、HTTP 监听。
11. 等待信号：`SIGINT`/`SIGTERM`（Windows 用 `Ctrl+C` 处理）→ 停核心 → 撤销 nft → 退出。

---

## 7. 数据结构（必须逐字段兼容）

### 7.1 数据目录布局

```
<data>/
  settings.json            面板设置
  subs.json                订阅列表（含 URL，权限 0600）
  template.json            sing-box 模板（用户可改）
  template.yaml            mihomo 模板
  config.json              渲染产物（sing-box）
  config.yaml              渲染产物（mihomo）
  nodes-<subID>.json       每个订阅解析出的节点
  snapshots/index.json     快照索引
  snapshots/v<N>-<unix>.json|yaml
  tproxy.nft               生成的 nft 规则（Linux）
  cache.db                 sing-box 的规则集缓存（由内核自己写）
  mihomo/                  mihomo 的工作目录（-d 指向这里）
    config.yaml            软链/副本不要求，直接 -f 指外层即可
    cache.db
    GeoSite.dat
    geoip.metadb
```

注意：`sing-box` 的 home 就是 `<data>` 本身；`mihomo` 的 home 是 `<data>/mihomo`。
两者**不同**，写代码时用一个 `engine::home(data_dir, engine)` 收敛，不要散落判断。

### 7.2 settings.json

字段名与类型必须完全一致（前端直接读这些字段）：

```jsonc
{
  "listen": "127.0.0.1:9090",
  "username": "admin",
  "password_hash": "<sha256 hex>",
  "engine": "sing-box",            // 或 "mihomo"
  "clash_addr": "127.0.0.1:9091",
  "login_disabled": false,
  "keep_snapshots": 3,
  "delay_ttl": 300,
  "delay_workers": 3,
  "tproxy_mode": "redirect",
  "tproxy_enabled": false,
  "tproxy_local": false,
  "tproxy_dns": false,
  "tun_enabled": false,
  "routing_mode": "rule",          // rule | global | direct
  "lan_proxy": false,
  "lan_socks_port": 1080,          // <=0 时补 1080
  "lan_http_port": 8118,           // <=0 时补 8118
  "lan_user": "",
  "lan_pass": "",
  "tproxy_port": 12345,            // <=0 时补 12345
  "lan_cidrs": null,               // 允许 null，读时按空处理
  "mixed_port": 7890,
  "mixed_listen": "127.0.0.1",
  "autostart_core": true,          // 指针类型，缺省视为 true
  "config_version": 1,
  "last_check_at": "",
  "last_check_ok": false,
  "lan_if": ""
}
```

**零值补齐规则**（`store.Open` 里逐条判断，缺一条老用户升级后就跑不起来）：
`keep_snapshots<=0→3`、`delay_workers<=0→3`、`delay_ttl<=0→300`、
`tproxy_port<=0→12345`、`clash_addr==""→"127.0.0.1:9091"`、`tproxy_mode==""→"redirect"`、
`routing_mode==""→"rule"`、`lan_socks_port<=0→1080`、`lan_http_port<=0→8118`、
`mixed_port<=0→7890`、`mixed_listen==""→"127.0.0.1"`。
`engine==""` 视为 `sing-box`（老 settings.json 没有这个字段）。

### 7.3 subs.json

```jsonc
[{ "id":"s1792512469573", "name":"xxx", "url":"https://...", "auto":true,
   "interval_hours":24, "node_count":28, "updated_at":"2026-11-02T...Z",
   "usage_used":0, "usage_total":0, "expire":0, "last_error":"" }]
```

- `id` 格式：`s` + 毫秒时间戳（保证唯一且可排序）。
- **原始订阅响应体不落盘**，只存解析后的节点。日志里 URL 必须打码。

### 7.4 nodes-<subID>.json

```jsonc
[{ "id":"n...", "tag":"🇺🇸 United States 01", "name":"...", "type":"vless",
   "server":"1.2.3.4", "port":443, "region":"US", "sub_id":"s179...",
   "enabled":true,
   "outbound": { /* 完整 sing-box outbound 片段，面板不做字段映射 */ } }]
```

`tag` 必须**全局唯一**（跨订阅也不能重名），否则 `sing-box check` 直接拒绝。
重名时追加 ` #2`、` #3` 后缀。

### 7.5 snapshots/index.json

```jsonc
[{ "version":3, "time":"...", "note":"apply", "size":36000, "file":"v3-1793611605.json" }]
```

文件名 `v<version>-<unix><ext>`，`ext` 跟随当前内核（`.json` / `.yaml`）。
超过 `keep_snapshots` 时删最旧的。

### 7.6 日志行

```jsonc
{ "at":"2026-11-02T18:08:23.089Z", "raw":"+0800 2026-11-02 18:08:23 INFO ..." }
```

环形缓冲 **400 行**，仅内存不落盘，写入前剥掉 ANSI 转义序列。

---

## 8. HTTP API（路径与字段一个都不能变）

统一前缀 `/api/v1`。鉴权：`Authorization: Bearer <token>`；
未启用登录时（`login_disabled` 或用户名与哈希都为空）不校验。

### 8.1 认证

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/auth/login` | 入 `{username,password}`，出 `{token}`；失败 401 |
| GET | `/auth/status` | 出 `{required,username,login_disabled}`（**不需要鉴权**） |
| GET | `/auth/check` | 校验 token，出 `{ok:true}` |
| PUT | `/auth/access` | 入 `{disabled,username,password}`，开关免密登录 |

- 密码存储：`sha256(username + ":" + password)` 的 hex。**不要换 bcrypt**，会破坏兼容。
- Token：随机 24 字节 hex，内存 map 保存，TTL **7 天**，最多 **64 个**（超出淘汰最旧）。
- 登录失败限流：同一 IP 连续失败要退避（简单计数即可，防止暴力破解）。

### 8.2 状态与日志

| 方法 | 路径 | 出参要点 |
| --- | --- | --- |
| GET | `/status` | 见下方字段表，**33 个字段全部必须返回** |
| GET | `/logs` | `{lines:[{at,raw}]}`，最多 500 行 |
| POST | `/service/{start\|stop\|restart}` | 操作内核进程，出 `{ok,error?}` |

`GET /status` 字段表（前端 `app.js` 的 `renderStatus()` 逐个读取，少一个就白屏）：

| 字段 | 类型 | 含义 / 来源 |
| --- | --- | --- |
| `running` | bool | 内核进程是否在跑 |
| `pid` | int | 内核 PID，未运行时 0 |
| `started_at` | string(RFC3339) | 内核启动时刻，零值表示从未启动 |
| `uptime` | int | 内核已运行秒数 |
| `version` | string | 形如 `sing-box 1.14.3` / `mihomo v1.19.33`，取不到时为空串 |
| `active` | string | 当前选中节点名（读 clash API 的 `proxies.proxy.now`），空串表示未选 |
| `panel_version` | string | 面板版本，Go 版是 `0.9.7` |
| `arch` | string | `runtime.GOOS + "/" + runtime.GOARCH`，如 `windows/amd64` |
| `platform` | string | 只 GOOS，如 `windows` |
| `mixed_port` | int | 混合代理端口，从渲染后的配置里读，**不是** settings |
| `mixed_listen` | string | 混合代理监听地址，来自 settings |
| `config_version` | int | 配置版本号，每次 apply 递增 |
| `nodes` | int | 节点总数 |
| `enabled` | int | 启用节点数 |
| `rules` | int | 规则条数，走 `config.RuleCount(raw, engine)`（两内核结构不同） |
| `rss` | int64 | **内核**常驻字节数 |
| `panel_rss` | int64 | **面板自己**常驻字节数 |
| `listen` | string | 面板监听地址，如 `0.0.0.0:9090` |
| `listen_pinned` | bool | 是否由 `-listen` 参数锁定（锁定时设置页不允许改） |
| `data_dir` | string | 数据目录绝对路径 |
| `login_required` | bool | 是否需要登录（`login_disabled` 为真、或用户名与哈希都为空时为 false） |
| `login_disabled` | bool | 是否关闭了登录 |
| `username` | string | 当前用户名 |
| `keep_snapshots` | int | 保留快照数 |
| `delay_workers` | int | 测延迟并发数 |
| `delay_ttl` | int | 延迟缓存有效秒数 |
| `tproxy` | string | `settings.tproxy_mode`，Go 版恒为 `"redirect"`（历史遗留，见 §8.6 末尾说明） |
| `subs` | int | 订阅数 |
| `last_check` | string | 上次配置校验输出；有 `last_check_at` 时置空（详情只在当次展示） |
| `last_check_ok` | bool | 上次校验是否通过 |
| `last_check_at` | string | 上次校验时间 |
| `last_error` | string | 内核最近一次异常/退出原因 |
| `panel_uptime` | int | 面板已运行秒数 |
| `now` | string(RFC3339) | 服务器当前时间，前端用来对时 |

注意两个 `rss` 的取值方式：Windows 用 psapi `GetProcessMemoryInfo` 的
`WorkingSetSize`，Linux 读 `/proc/<pid>/statm` 的第 2 个字段乘页大小。
内核进程拿不到时返回 0，**不要返回 -1 或 null**（前端直接做除法）。

### 8.3 订阅 / 节点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/subscriptions` | 列表 |
| POST | `/subscriptions` | 新增 `{name,url,auto,interval_hours}` |
| PUT | `/subscriptions/{id}` | 改名/改 URL/开关自动更新 |
| DELETE | `/subscriptions/{id}` | 删除（连带删 nodes-<id>.json） |
| POST | `/subscriptions/{id}/refresh` | 立即拉取 |
| GET | `/nodes` | 节点 + 内存中的延迟缓存 |
| POST | `/nodes/delay` | 入 `{ids:[...]}` 或 `{all:true}`，并发上限 `delay_workers` |
| PUT | `/nodes/{id}` | 入 `{enabled}`，切换单节点启用 |

### 8.4 配置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/config` | 出 `{raw,path,engine,dirty}` |
| POST | `/config/render` | 用模板 + 节点重新渲染，返回文本，**不落盘** |
| POST | `/config/validate` | 入 `{raw}`，调内核自己的 check，出 `{ok,output,elapsed_ms}` |
| POST | `/config/apply` | 入 `{raw}`，先快照 → 写盘 → 重启 → 探活；失败自动回滚 |
| GET | `/config/snapshots` | 快照列表 |
| POST | `/config/rollback` | 回滚到上一份可用配置 |

### 8.5 内核 / geo

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/engine` | 出 `{engine,label,config,available:[{name,label,bin,version,version_short,active,ok}]}`；`available` 是**数组**不是对象，固定两项 |
| PUT | `/engine` | 入 `{engine}`，切换内核（见 §11.5） |
| GET | `/geodata` | 出 `{geo:{state,dir,done,total,current,bytes,total_bytes,missing,embedded,at}}` |
| POST | `/geodata/prefetch` | 手动触发预取，立即返回，进度靠轮询 `/geodata` |

`state` 取值：`idle` / `downloading` / `ready` / `failed`。
`version` 是内核 `--version` 的**完整首行**（如 `Mihomo Meta v1.19.33 windows amd64 with go1.26.10 ...`），
`version_short` 是提取出的纯版本号（`1.14.3` / `v1.19.33`）。两个都要给，前端两个都用。
`missing` 是缺文件的相对名数组，全部就绪时为 `null`。
`embedded` 表示这次是从编译期内置副本铺出来的。

### 8.6 网络接管

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/PUT | `/tproxy` | 透明代理（Linux）。GET 出 `{enabled,supported,local,local_supported,dns,mode,port,rules,cidrs,detected,note}`；PUT 入 `{enabled,local?,dns?,cidrs?}` |
| GET/PUT | `/lanproxy` | 局域网带鉴权代理。出 `{enabled,open,mixed_open,mixed_listen,mixed_port,mixed_url,socks_port,socks_url,http_port,http_url,username,password,has_password}` |
| GET/PUT | `/tun` | TUN。出 `{enabled,running,supported,platform,note}`；PUT 入 `{enabled}` |
| GET/PUT | `/autostart` | 出 `{panel,core,panel_supported,panel_note,core_note,platform}` |
| GET/PUT | `/routing` | 出 `{mode,name,note}`；PUT 入 `{mode:"rule"\|"global"\|"direct"}` |
| PUT | `/settings/listen` | 入 `{listen}`，改监听地址（支持热切换，不重启面板） |

**三个「模式」不是一回事，别混**：

| 概念 | 字段 | 取值 | 接口 |
| --- | --- | --- | --- |
| 路由模式（走不走节点） | `settings.routing_mode` | `rule` / `global` / `direct` | `/routing` |
| TUN 开关（虚拟网卡接管） | `settings.tun_enabled` | bool | `/tun` |
| 透明代理开关（nftables） | 无持久字段，看运行时 | bool | `/tproxy` |

`/status` 里的 `tproxy` 字段读的是 `settings.tproxy_mode`，Go 版**没有任何接口会写它**，
永远是默认值 `"redirect"`，只是历史遗留字段。重构时保留默认值即可，别为它设计 UI。

### 8.7 WebSocket 与代理透传

| 路径 | 说明 |
| --- | --- |
| `GET /ws/connections` | 每 2 s 推一帧连接列表（转发 Clash `/connections`，只保留前 200 条） |
| `GET /ws/traffic` | 每 2 s 推 `{up,down,ts}`，数据源是 Clash 的 `/traffic` 流 |
| `ANY /clash/*` | 白名单透传到 Clash API：`/proxies`、`/connections`、`/traffic`、`/version`、`/configs`、`/rules` |

WS 鉴权：浏览器 `WebSocket` 不能带 header，token 走 query `?token=<token>`。

---

## 9. 双内核差异表（写成一个 `engine` 模块，不要散落 if）

| 项 | sing-box | mihomo |
| --- | --- | --- |
| 配置文件名 | `config.json` | `config.yaml` |
| 模板文件名 | `template.json` | `template.yaml` |
| home 目录 | `<data>` | `<data>/mihomo` |
| 启动 | `run -c <conf>` | `-d <home> -f <conf>` |
| 校验 | `check -c <conf>` | `-t -d <home> -f <conf>` |
| 配置格式 | JSON | YAML |
| 透明代理 | 模板里的 `redirect` 入站 | `redir-port` + `listeners` 声明 |
| geo 文件 | 不需要（规则集自己下 `.srs`） | 需要 `GeoSite.dat` + `geoip.metadb` |
| 延迟测量口径 | 每次新建连接（含完整 reality 握手，~579 ms） | 复用连接池（~145 ms） |

**注意**：两个内核测同一节点的延迟能差 4–6 倍，切换内核后**必须清空延迟缓存**，
否则用户会拿旧内核的数值判断线路质量。

---

## 10. 渲染规则（最容易写错的地方）

模板由用户维护，面板只做「JSON/YAML 合并 + 占位符替换 + 少量字段覆盖」。
渲染时必须实现下面这些覆盖与迁移，缺一个就会出现"用户配置能通过校验但跑不起来"。

### 10.1 必须做的覆盖

1. **节点注入**：模板里 `outbounds` 中 `type:"selector"` 和 `type:"urltest"` 的
   `outbounds` 数组，替换成「`proxy`（selector）+ 全部启用节点 + `direct`」。
   只注入 `enabled=true` 的节点，顺序按 `nodes-*.json` 里的顺序。
2. **rule-set 下载出站**：若 `route.rule_set` 里有 `http_client` 或 `download_detour`，
   必须在 `outbounds` 里补一个 `{"type":"direct","tag":"rule-set","tcp_fast_open":true}`，
   并把 detour 指到它。**不能指向空 direct**（sing-box 报
   `detour to an empty direct outbound makes no sense` 拒绝启动），
   也不能指向 `proxy`（节点全挂时下不到规则集 → FATAL → 崩溃循环）。
3. **mixed 入站端口覆盖**：模板里写死的 `127.0.0.1:7890` 要按
   `settings.mixed_listen` / `mixed_port` 覆盖。
4. **路由模式**：
   - `rule`（默认）：模板原样
   - `global`：`route.final = "proxy"`，并清空/禁用分流规则（保留 dns 劫持与私有地址直连）
   - `direct`：`route.final = "direct"`
5. **局域网代理**：开启时在 `inbounds` 追加带 `username/password` 的 socks 与 http 入站。
6. **TUN**：开启时追加 `tun` 入站（`auto_route: true`，`stack: "system"`，`strict_route` 按平台）。
7. **mihomo 专用**：
   - `redir-port` 必须配合 `listeners: [{type: redir, listen: 0.0.0.0}]`，
     只写 `redir-port` 在部分版本会被 `bind-address`/`allow-lan` 支配成只绑回环。
   - `clash-api` 监听地址取自 `settings.clash_addr`。
   - `cache_file.path`、`geox-url` 用绝对路径。

### 10.2 路径处理

模板里的相对路径（如 `cache_file.path: "cache.db"`）解析基准是**核心子进程的工作目录**，
跨平台不可靠。渲染时统一转成基于 `data_dir` 的绝对路径。

### 10.3 模板迁移（幂等，每次打开数据目录都跑）

只碰下面几处，且必须精确匹配内置默认值，用户自己改过的一律不动：

1. `https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs`
   → `https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-cn.srs`
2. 同上 `sing-geoip` → `cdn.jsdelivr.net/gh/SagerNet/sing-geoip@rule-set/geoip-cn.srs`
3. `rule_set[].http_client.detour` 为 `proxy` 或 `direct` → 改成 `rule-set`
4. **老字段 `rule_set[].download_detour`（值为 `proxy`/`direct`）**：
   删掉该字段，写入 `http_client: {detour: "rule-set"}`

> 实测教训：第 4 条漏掉时，渲染层会把 `download_detour: proxy` 改写成
> `http_client.detour: proxy`，于是「节点未就绪 → 规则集下载失败 → 核心 FATAL →
> 崩溃循环 → 切内核回滚」这套故障就复现了。用户只能看到"切换失败"。

---

## 11. 进程管理

### 11.1 拉起

- 子进程**必须**：工作目录 = 内核 home；stdout+stderr 合并捕获。
- Windows：创建时加入 **Job Object**（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`），
  保证面板被杀时内核一起死，不留孤儿。
- Unix：`setsid` 起新进程组，退出时 `kill(-pgid, SIGTERM)`，5 s 后 `SIGKILL`。

### 11.2 日志

读子进程输出 → 剥 ANSI → 推入 400 行环形缓冲 → 同时写面板自己的 `log`。
注意行可能被截断（内核一次写半行），要按 `\n` 缓冲拼行。

### 11.3 崩溃处理

- 10 分钟窗口内崩溃超过 **3** 次 → 停止自动重启，触发 `on_give_up`：
  撤销透明代理规则（让主机回落到直连，而不是死等一个不存在的端口）。
- 崩溃后重启间隔 2 s。
- 每次成功启动并探活通过后，清空崩溃计数。

### 11.4 孤儿清理（`sweep_orphans`）

面板启动时，扫描与**当前数据目录**关联的同名内核进程并杀掉。
判定条件必须包含"同一 data 目录"（比如看命令行里是否带该目录路径），
**不能只按可执行文件名杀** —— 用户可能手动起了同款二进制做别的实验。

### 11.5 切换内核（`PUT /engine`）

顺序不能错：

1. 校验目标内核可执行文件存在且能跑（`--version`）。
2. **若目标是 mihomo**：先 `ensure_geo()`，把 geo 数据备好（见 §13）。
   失败不算致命，记日志让 mihomo 自己下。
3. 停当前内核 → 按目标引擎重新渲染配置（写 `config.json` 或 `config.yaml`）→ 启动 → 探活。
4. 探活失败：自动回滚到上一份配置并切回原内核，返回明确错误。
5. 成功后：**清空延迟缓存**（§9 末尾），更新 `settings.engine`。
6. **若目标切回 sing-box**：后台预取 geo（给下次切 mihomo 用）。

---

## 12. 订阅解析

自动识别三种格式（按顺序嗅探）：

1. **Clash YAML**：body 含 `proxies:` → 用 `serde_yaml` 解析 `proxies[]`，
   逐条转成 sing-box outbound（支持 ss/ssr/vmess/vless/trojan/hysteria2/tuic）。
2. **Base64(URI 列表)**：整体 base64 解码后按行解析 `ss://`、`vmess://`、
   `vless://`、`trojan://`、`hysteria2://`。
3. **sing-box JSON**：body 是 JSON 且含 `outbounds` → 直接搬运（过滤掉
   `direct`/`block`/`selector`/`urltest` 这类内置出站）。

规则：

- **outbound type 归一**：URI scheme 是 `ss`，但 sing-box 只认 `shadowsocks`。
  渲染时也要再归一一次（库里可能存着老数据）。
- 过滤「剩余流量：xx GB」「套餐到期：xx」这类占位条目（不是节点）。
- `tag` 全局唯一，重名加 ` #2` 后缀。
- 失败要写进 `last_error`，不要静默。
- 日志里 URL 必须打码（只显示 host）。

---

## 13. geo 数据（mihomo 专用）

mihomo 缺 `GeoSite.dat` / `geoip.metadb` 会自己下载，实测 **31 秒**
（GeoSite 5 s + MMDB 25 s），这期间"切内核"一直卡着，用户以为程序挂了。

三层兜底，缺一不可：

1. **编译期**：`assets/GeoSite.dat.gz` + `assets/geoip.metadb.gz` 用 `include_bytes!` 打包。
   **必须 gzip**：原始 12 MB 会被读进内存常驻，压缩后只有 5 MB。
2. **空闲预取**：面板启动、以及切到 sing-box 时后台下载（并行两个文件）。
3. **切换前 ensure**：文件已在就直接返回（0 ms），否则下载或解压。

关键实现细节：

- **文件映射关系是实测出来的，不要想当然**：mihomo 工作目录里叫 `geoip.metadb`，
  但它的内容其实是 `country.mmdb`（MMDB 格式），不是 v2ray 的 `geoip.dat`。
  下载 URL 必须是 `.../country.mmdb`。
- 解压/写入**必须流式**（32 KB 缓冲），不能整份读进内存再写盘。
- 大小阈值校验要用**解压后**的字节数（压缩后比阈值小是正常的）。
- 慢速换源：8 秒窗口内平均速度低于 48 KB/s → 切换镜像重下。
  镜像：`cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/` ↔
  `gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/raw/release/`。
- 并发调用要**单飞**（single-flight）：多个 `ensure` 共享同一次下载。
- 状态推导以**磁盘为准**：文件齐全就是 `ready`，不要只信内存里的历史状态。

---

## 14. 平台层

### 14.1 透明代理（Linux）

- 用 `nft` 命令下发，`table inet singbox`（表名可配）。
- `chain prerouting`：`ip saddr { <cidrs> } meta l4proto tcp redirect to :<port>`；
  可选劫持 LAN 的 53 端口到 DNS 入站。
- `chain output`（可选，接管本机）：排除面板自身进程，避免回环。
- **能力探测**：不要只看 `/sys/module/nft_chain_nat` 是否存在
  （`CONFIG_NF_NAT_REDIRECT=y` 时没有这个目录）。正确做法是拿一段探测规则
  `nft -c -f -` 做真实解析。
- 关闭时删表；面板退出/核心未运行时也要删表（否则断网）。

### 14.2 TUN（Windows）

- 需要管理员权限。`TunSupported()` 用一次轻量的令牌检查判断。
- 非管理员时**必须降级**：不渲染 tun 入站、关掉设置里的开关、写日志。
  否则 wintun 建卡失败会让核心 FATAL 崩溃循环，连 mixed 入站也起不来。
- 虚拟网卡名：sing-box 用 `singbox-tun`，mihomo 用 `mihomo-tun`（mihomo 自带 wintun）。

### 14.3 开机自启

- **Windows**：`schtasks` 注册计划任务（SYSTEM 身份 + BootTrigger +
  失败 1 分钟后重启），用完整任务 XML 导入，不要用 `/SC ONSTART` 的简写。
- **Linux**：systemd unit `/etc/systemd/system/singbox-panel.service`，
  `systemctl enable/disable`，unit 缺失时自动生成。
- **macOS**：LaunchAgent plist。

面板自启与核心自启是**两个独立开关**：面板不自启时，核心也不会被拉起。

---

## 15. 前端资源

- `include_bytes!` 打包 `index.html` / `app.js` / `styles.css`。
- **ETag 必须由内容哈希算出**，不要写死版本号：改前端忘改版本号时，
  浏览器会一直命中 304 看到旧界面（这个坑踩过）。
- 未命中路径回退到 `index.html`（前端是 hash 路由）。
- `/api/*` 未知路径返回 JSON 404，不要掉进静态处理器返回 HTML。

---

## 16. 内存与体积预算（验收标准）

在**不牺牲功能**的前提下，必须达到：

| 指标 | Go 版（实测） | Rust 版要求 |
| --- | --- | --- |
| 二进制（含 geo） | 15.8 MB | **≤ 6 MB** |
| 二进制（不含 geo） | 10.5 MB | **≤ 3.5 MB** |
| 常驻内存 Linux 空闲 | 12–16 MB | **≤ 8 MB** |
| 常驻内存 Windows 空闲 | 16–22 MB | **≤ 12 MB** |
| 空闲 90 s 后 | 不回落 | **回落到上表水平** |

实现手段（按收益排序）：

1. `[profile.release]`：`opt-level="z"`、`lto="fat"`、`codegen-units=1`、`panic="abort"`、`strip=true`。
2. geo 用 gzip 打包（12 → 5 MB），解压走流式。
3. 避免 `serde_json::Value` 满天飞：热路径（`/status`、`/logs`）用 `#[derive(Serialize)]` 结构体。
4. 日志环形缓冲用 `VecDeque<String>` + 复用缓冲，不要每次 `format!` 新串。
5. **空闲回收**：90 s 无请求时调 `malloc_trim(0)`（glibc）或
   `madvise(MADV_DONTNEED)`，把空闲页还给系统。注意 musl 没有 `malloc_trim`，
   Linux musl 目标下可用 `mallopt` 或直接跳过（musl 的分配器本身就比较激进地还页）。
6. 不要用 `lazy_static` 存大对象；模板等只在使用时读取。

---

## 17. 测试要求

必须覆盖（用 `cargo test`，不依赖网络）：

1. **渲染**：给定模板 + 节点 → 断言 outbound 注入正确、rule-set detour 指向 `rule-set`、
   mixed 端口被覆盖、三种 routing mode 的 `final` 正确。
2. **模板迁移**：老字段 `download_detour: proxy` 被迁走、URL 换成镜像、**幂等**
   （第二次打开文件不变）。
3. **订阅解析**：Clash YAML / Base64 URI / sing-box JSON 三种输入各一个用例；
   `ss` → `shadowsocks` 归一；tag 去重。
4. **geo**：gzip 解压内容逐字节一致；解压后过小要报错且不留残file；
   原始文件（未压缩）也能用；并发 ensure 只发一次请求。
5. **store**：原子写、快照轮转（超过上限删最旧）、老 settings.json 缺字段补默认。
6. **engine**：文件名/home/命令行参数映射正确。
7. **auth**：密码哈希、token TTL 与上限、免密模式。

---

## 18. 分阶段实施（每阶段都要能编译通过并自测）

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| 1 | CLI + store + 设置读写 + 静态前端 embed | 能起服务，登录页能打开 |
| 2 | auth + status + logs + supervisor | 能拉起内核、看日志、看状态 |
| 3 | subscription（三格式）+ nodes + clash 客户端 | 能加订阅、解析出节点、测延迟 |
| 4 | config render（sing-box）+ apply/validate/rollback | 能渲染出可 `check` 通过的配置并启动 |
| 5 | mihomo 引擎 + geo 三层兜底 | 双向切内核 < 2 s |
| 6 | tproxy（Linux）+ TUN（Windows）+ 自启 | 透明代理与 TUN 可用 |
| 7 | 内存/体积优化 + 全平台交叉编译 + CI | 达到 §16 指标 |

---

## 19. 交付清单

- [ ] `cargo build --release` 无 warning
- [ ] 全部测试通过
- [ ] 六个目标平台产物 + `make release`
- [ ] 能用**现有 Go 版的 `panel-data/` 目录**直接启动，订阅/设置/快照全部还在
- [ ] 前端零改动，所有页面功能正常（概览/订阅/节点/配置/服务/网络/局域网代理/连接/设置）
- [ ] 双内核双向切换 < 2 s，切换失败能回滚
- [ ] Linux 透明代理、Windows TUN 实测可用
- [ ] 达到 §16 的内存与体积指标
- [ ] README 更新：构建方式、产物矩阵、内存实测数据

---

## 20. 明确不要做的事

- 不要重新设计前端，不要改 API 路径或字段名。
- 不要引入数据库（现在全是 JSON 文件，改 SQLite 会让数据目录不兼容）。
- 不要用 `unwrap()` 处理任何来自网络或磁盘的输入。
- 不要在热路径里 `clone()` 大字符串（日志、配置全文）。
- 不要把 `panic="abort"` 当兜底：HTTP handler 里的 panic 会让整个面板退出。
  用 `tower` 的 `CatchPanicLayer` 或自己包一层 `catch_unwind`。
- 不要为了省内存把 tokio 换成单线程后不测并发：面板同时跑 HTTP、两个 WS、
  子进程 pump、定时器，单线程容易在"切内核"时把接口卡住。
