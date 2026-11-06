@echo off
rem 由计划任务调用，让面板脱离当前登录会话常驻运行。
cd /d "C:\Users\Administrator\Desktop\sing-box-web"
panel-windows-amd64.exe -data "C:\Users\Administrator\Desktop\sing-box-web\panel-data" -listen 0.0.0.0:9090 -clash 127.0.0.1:9091 -engine sing-box -sing-box "C:\Users\Administrator\Desktop\sing-box-web\sing-box.exe" -mihomo "C:\Users\Administrator\Desktop\sing-box-web\mihomo.exe" -password admin123456 >> "C:\Users\Administrator\Desktop\sing-box-web\panel-run.log" 2>&1
