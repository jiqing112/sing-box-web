sing-box panel · Linux 免安装包
================================

包里有什么
----------
  panel                     面板本体（Web 控制台，已内嵌前端）
  sing-box                  官方 sing-box 内核（安装脚本会放到 /usr/local/bin）
  panel.conf                面板配置：监听地址 / 登录账户 / 密码
  sing-box-panel.service    systemd 单元
  install.sh                一键安装脚本
  README.txt                本文件


一键安装
--------
  tar -xzf sing-box-web-*-linux-*.tar.gz
  cd sing-box-web-*-linux-*
  sudo ./install.sh

脚本会做这些事：
  1. 面板装到 /opt/sing-box-panel/bin/panel，数据在 /opt/sing-box-panel/data
  2. sing-box 内核装到 /usr/local/bin/sing-box
  3. 安装并启用 systemd 服务 sing-box-panel（开机自启）
  4. 首次启动生成随机密码，并打印出来（也在 /opt/sing-box-panel/data/initial-password）

安装完访问 http://<本机IP>:9090 ，用 admin + 打印出来的密码登录。

  登录后： 「订阅」页添加订阅 → 「配置」页 生成 / 校验 / 应用
  默认代理端口： mixed 127.0.0.1:7890（仅本机）
  局域网要用： 「透明代理」页开启「局域网代理」并设置用户名密码


手动安装（不用脚本）
--------------------
  install -d /opt/sing-box-panel/{bin,data}
  install -m755 panel /opt/sing-box-panel/bin/panel
  install -m755 sing-box /usr/local/bin/sing-box
  install -m600 panel.conf /opt/sing-box-panel/bin/panel.conf
  install -m644 sing-box-panel.service /etc/systemd/system/
  systemctl daemon-reload && systemctl enable --now sing-box-panel


升级
----
  解压新包后重新执行 sudo ./install.sh 即可。
  已有的 panel.conf 与 /opt/sing-box-panel/data 不会被覆盖（配置、订阅、快照都保留）。


常见问题
--------
· 想改监听端口 / 口令
  改 /opt/sing-box-panel/bin/panel.conf 后 systemctl restart sing-box-panel。
  注意 unit 里写死了 -listen 0.0.0.0:9090，命令行参数优先于 panel.conf；
  要永久改端口请同时修改 unit 的 ExecStart。

· 忘记密码
  在 panel.conf 里把 password= 改成新密码，重启面板即可。

· 透明代理（nftables）
  「透明代理」页可以接管局域网 TCP 流量与 DNS，需要 root（systemd 服务默认就是 root）。
  开启前请确认本机没有别的代理软件占用 7890 / 12345 / 5354 / 53 端口。

· TUN 模式
  Linux 上同样需要 root 与 /dev/net/tun，开启后整机流量走虚拟网卡。

· 日志
  journalctl -u sing-box-panel -f


目录说明
--------
  /opt/sing-box-panel/bin/panel.conf     面板配置
  /opt/sing-box-panel/data/settings.json 面板设置
  /opt/sing-box-panel/data/template.json 配置模板
  /opt/sing-box-panel/data/config.json   实际下发给 sing-box 的配置
  /opt/sing-box-panel/data/snapshots/    配置快照（默认 3 份，可回滚）
  /opt/sing-box-panel/data/initial-password  首次启动生成的密码，登录后建议删除
