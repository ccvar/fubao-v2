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
