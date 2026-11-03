# geo 数据内置目录

把 `GeoSite.dat.gz` 和 `geoip.metadb.gz` 放进这个目录再编译，它们会被 embed 进面板二进制，
mihomo 切内核时**零下载、离线可用**。

```sh
cd app/internal/service/geodata
./fetch.sh          # 下载并 gzip（推荐）
./fetch.sh --raw    # 下载后不压缩，调试用
./fetch.sh --clean  # 清空，回到「运行期下载」模式
```

## 为什么压缩

两个文件原始合计约 12 MB，gzip 后约 5 MB。压缩不只是为了仓库体积：embed 的数据
**一旦被读过就会常驻进程工作集**，所以打原始文件等于让面板常驻 RSS 多挂 12 MB，
压缩后只有 5 MB。面板解压时走 `io.Copy` 流式写盘，不会把整份文件读进堆。

未压缩的原始文件也支持（`--raw`，或手工放进去），`openEmbedded` 会先找 `.gz` 再退回原文件。

不打包也能用：面板启动后会在后台把这两个文件下载到 mihomo 工作目录
（`<data>/mihomo/`），切内核时如果还没就绪会等它下完。

默认仓库里只有这个说明文件（`.gitignore` 已排除 `*.dat` / `*.mmdb` / `*.gz`），
所以正常构建不会把 12 MB 数据带进版本库。
