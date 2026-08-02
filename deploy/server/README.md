# 福宝远程同步服务

服务端与桌面客户端位于同一个仓库，但分别编译为 `fubao-sync-server` 和 `fubao-engine`。服务端只接收安全的直播间状态与红包事件，不接收 Cookie、签名、请求头、原始响应或参与账号凭证。

## 一键安装

第一版安装器支持 Debian/Ubuntu 的 amd64 与 arm64：

```sh
curl -fsSL https://raw.githubusercontent.com/ccvar/fubao-v2-releases/main/install-sync-server.sh | sudo sh
```

安装器会安装服务端、官方 Caddy 软件包、systemd 单元和 `fbv2.ccvar.com` 反向代理。安装前需要把域名 A/AAAA 记录指向服务器，并开放 TCP 80、443、8087。标准 HTTPS 入口不可用时，客户端会通过健康检查自动降级到 HTTPS 8087 入口。

安装完成后终端会输出一次客户端注册令牌。客户端 Go 数据目录中的 `remote_sync.json` 使用以下结构：

```json
{
  "version": 1,
  "enabled": true,
  "endpoint": "https://fbv2.ccvar.com/api/v1",
  "fallback_endpoint": "https://fbv2.ccvar.com:8087/api/v1",
  "enrollment_token": "安装器输出的注册令牌"
}
```

首次连接成功后，客户端会用独立设备令牌替换注册令牌。两个令牌都只保存在权限为 `0600` 的 Go 数据文件中。

未绑定同步 KEY 的客户端会通过公开的自动注册入口取得“仅上传”设备令牌。该令牌只允许把安全白名单中的直播间与红包数据写入中心库，不能读取中心增量；自动注册按来源地址限速。绑定安装器生成的同步 KEY 后，服务端才会把同一客户端升级为完整设备身份。客户端随后按本机授权状态决定接收范围：无有效授权不拉取、有限期有效授权只拉取红包、永久有效授权拉取直播间与红包。

## 原地升级

服务器后续升级继续执行同一条一键安装命令：

```sh
curl -fsSL https://raw.githubusercontent.com/ccvar/fubao-v2-releases/main/install-sync-server.sh | sudo sh
```

升级器会通过 SQLite 在线备份在 `/var/lib/fubao-sync/backups/` 创建升级前快照、保留现有数据库与注册令牌，原子替换二进制后明确重启 systemd 服务。新版本需要的数据库表会在首次启动时自动迁移，无需手工导入。

升级后确认版本和服务状态：

```sh
curl -fsS https://fbv2.ccvar.com/healthz | python3 -m json.tool
systemctl status fubao-sync --no-pager
```

## 运维

```sh
systemctl status fubao-sync
journalctl -u fubao-sync -f
curl https://fbv2.ccvar.com/healthz
curl https://fbv2.ccvar.com:8087/healthz
```

SQLite 数据库位于 `/var/lib/fubao-sync/fubao-sync.db`，Caddy 配置片段位于 `/etc/caddy/conf.d/fbv2.caddy`。

查看同步概况：

```sh
curl -s https://fbv2.ccvar.com/healthz | python3 -m json.tool
```

查看最近更新的直播间：

```sh
sqlite3 -header -column /var/lib/fubao-sync/fubao-sync.db \
  "SELECT web_rid, streamer_name, title, live_status, monitor_status, updated_at FROM rooms ORDER BY updated_at DESC LIMIT 30;"
```

查看最近同步的红包：

```sh
sqlite3 -header -column /var/lib/fubao-sync/fubao-sync.db \
  "SELECT web_rid, packet_id, title, prize, detected_at, expires_at FROM red_packet_events ORDER BY detected_at DESC LIMIT 30;"
```

明细数据库只允许在服务器本机读取。不要把 SQLite 文件、设备令牌或注册令牌放到公开下载目录。
