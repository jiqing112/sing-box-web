package store

// defaultClashTemplate 是 mihomo 引擎的内置模板（YAML 文本）。
//
// 面板只往这份模板里注入 proxies / proxy-groups / 端口 / TUN / 路由规则，
// 其余字段（DNS、日志级别、缓存策略）都可以直接在「配置」页里改。
// 模板按原样落到 data/template.yaml，用户改坏了可以删掉文件让面板重建。
func defaultClashTemplate() string {
	return `# mihomo 模板（YAML）。面板会覆盖以下字段，手改这些字段不会生效：
#   proxies / proxy-groups / rules（规则分流模式下） / mixed-port / bind-address
#   allow-lan / redir-port / dns.listen / tun / listeners
mixed-port: 7890
allow-lan: false
bind-address: 127.0.0.1
mode: rule
log-level: info
ipv6: false
external-controller: 127.0.0.1:9091
unified-delay: true
tcp-concurrent: true
find-process-mode: 'off'
profile:
  store-selected: true
  store-fake-ip: false

# geo 数据源。mihomo 默认从 github.com 下载 geosite.dat / country.mmdb，
# 国内直连经常失败（表现为首次应用配置卡住 90 秒后报错），这里换成 jsDelivr CDN。
# 下载结果会缓存到数据目录，只在第一次用到时联网；内网离线环境可以提前把
# GeoSite.dat / geoip.metadb 拷进数据目录，或把这里改成自己的镜像。
geox-url:
  geoip: https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geoip.dat
  geosite: https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat
  mmdb: https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/country.mmdb

dns:
  enable: true
  ipv6: false
  # 局域网 DNS 接管（nft 把 LAN 的 53 端口重定向到这里）
  listen: 0.0.0.0:5354
  enhanced-mode: redir-host
  nameserver:
    - 223.5.5.5
    - 119.29.29.29
  # 解析节点域名用国内 DNS，避免「DNS 走代理、代理要解析域名」的死循环
  proxy-server-nameserver:
    - 223.5.5.5

proxies: []
proxy-groups: []
rules:
  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve
  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve
  - IP-CIDR,100.64.0.0/10,DIRECT,no-resolve
  - IP-CIDR,169.254.0.0/16,DIRECT,no-resolve
  - GEOSITE,cn,DIRECT
  - GEOIP,CN,DIRECT
  - MATCH,proxy
`
}
