#!/bin/sh
# 把 mihomo 需要的 geo 数据下载到 assets 目录，下次编译就会 embed 进面板二进制。
#
#   ./fetch.sh          # 下载并 gzip（推荐）
#   ./fetch.sh --raw    # 下载后不压缩（调试用，二进制会大 8 MB）
#   ./fetch.sh --clean  # 删掉，恢复成「运行期下载」模式
#
# 两个文件合计约 12 MB，gzip 后约 4 MB。压缩不只是为了仓库体积：embed 的
# 数据一旦被读过就会常驻进程工作集，压过的副本只会把 4 MB 拉进 RSS 而不是 12 MB。
# 不下载也能编译：面板启动后会在后台自己下到 <data>/mihomo/，只是首次切换内核要等几秒。
set -eu

cd "$(dirname "$0")/assets"

if [ "${1:-}" = "--clean" ]; then
    rm -f GeoSite.dat geoip.metadb GeoSite.dat.gz geoip.metadb.gz
    echo "已清理，编译出的面板将依赖运行期下载"
    exit 0
fi

raw=0
[ "${1:-}" = "--raw" ] && raw=1

base=https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release

fetch() {
    # $1 = 目标文件名，$2 = 源文件名
    if [ -s "$1" ] || [ -s "$1.gz" ]; then
        echo "已存在 $1（或 $1.gz），跳过"
        return
    fi
    echo "下载 $2 -> $1"
    curl -fSL --retry 3 --max-time 300 -o "$1.part" "$base/$2"
    mv "$1.part" "$1"
}

fetch GeoSite.dat geosite.dat
fetch geoip.metadb country.mmdb

if [ "$raw" = 0 ]; then
    for f in GeoSite.dat geoip.metadb; do
        [ -s "$f" ] || continue
        gzip -9 -c "$f" > "$f.gz.part"
        mv "$f.gz.part" "$f.gz"
        rm -f "$f"
    done
    ls -lh GeoSite.dat.gz geoip.metadb.gz
    echo "完成（已压缩）。重新编译即可让面板自带 geo 数据。"
else
    ls -lh GeoSite.dat geoip.metadb
    echo "完成（未压缩）。重新编译即可让面板自带 geo 数据。"
fi
