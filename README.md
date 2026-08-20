# SNI Proxy

SNI Proxy 是面向 Linux amd64 的 HTTP Host / TLS SNI 代理，附带一个本机管理后台和跨 Init System 的 root CLI 管理层。

## 安全默认值

- HTTP/HTTPS 代理默认监听 `0.0.0.0:80/443`；管理后台独立监听 `127.0.0.1:6866`。
- `allowed_domains` 为空时拒绝全部代理目标。安装必须显式提供域名 allowlist。
- 上游解析使用一次总超时预算，并拒绝 RFC1918、CGNAT、documentation、benchmark、reserved、loopback、link-local 等 special-use 地址。
- HTTP 入口由 Go `http.Server`/`ReverseProxy` 逐请求解析和路由；TLS 入口仅解析有边界的 ClientHello SNI。
- 全局和每客户端 IP 的连接上限默认开启。
- 管理后台不执行服务启停、端口修改或更新。高权限写操作只允许 root CLI 执行。
- 管理员密码使用 Argon2id；登录有每 IP token bucket、失败退避、全局校验并发上限和 64 个会话上限。
- 更新仓库固定为 `CareyChi/sni-proxy`；安装包必须由 root-owned 信任策略中的 Ed25519 公钥验证。
- 更新事务包含 payload/ELF 校验、owner/mode、`CAP_NET_BIND_SERVICE` 设置与复核、服务注册、重启、三个监听端口 readiness；失败自动恢复旧目录、旧服务注册并复核旧版本健康。
- 安装/卸载只接受规范化且位于指定管理空间的路径；四个目录必须互不重叠，并由匹配同一随机 installation ID 的 root-owned marker 保护。

远程访问管理后台时，建议使用 SSH tunnel、WireGuard/Tailscale，或让 Caddy/Nginx 在 HTTPS 层终止 TLS 后反代到 `127.0.0.1:6866`。反代部署需显式设置 `admin_public_url` 和 `cookie_secure=true`；程序不会信任任意来源的 `X-Forwarded-Proto`。

## 默认目录

| 用途 | 默认路径 | Owner |
|---|---|---|
| 程序、脚本、服务模板 | `/opt/sni-proxy` | `root:root` |
| 运行配置和更新信任策略 | `/etc/sni-proxy` | `root:root` |
| 凭据与备份 | `/var/lib/sni-proxy` | `sni-proxy:sni-proxy` |
| 日志 | `/var/log/sni-proxy` | `sni-proxy:sni-proxy` |

## 安装

本仓库不提交编译产物，构建要求 Go 1.25.13 或更高补丁版本。先构建静态 Linux 二进制并放入 `bin/sni-proxy`，再设置允许的目标域名：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/sni-proxy ./cmd/sni-proxy
sudo SNI_PROXY_ALLOWED_DOMAINS='example.com,example.net' ./scripts/install.sh
```

若通过 `SNI_PROXY_PACKAGE_URL` 远程下载安装包，还必须通过 `SNI_PROXY_RELEASE_PUBLIC_KEY_FILE` 提供 root-owned Ed25519 PEM 公钥；安装器会验证签名 manifest，并把原始公钥写入 root-owned `update-trust.json`。本地 payload 安装未配置发布公钥时仍可完成，但 `update apply` 会保持 fail-closed。

安装完成后管理后台只可从本机访问：

```sh
ssh -L 6866:127.0.0.1:6866 root@server
# 浏览器打开 http://127.0.0.1:6866
```

## 管理

交互式管理入口：

```sh
sudo sni-proxyctl
```

等价 root CLI 示例：

```sh
sudo sni-proxy service restart
sudo sni-proxy config set-ports --http 8080 --https 8443 --web 6866
sudo sni-proxy update apply --version v1.2.3
sudo sni-proxy admin reset-credentials
```

密码重置默认通过 TTY 隐藏输入；自动化场景使用 `--password-file`、`--password-stdin` 或显式 `--generate`。不建议使用会暴露在 argv 和 shell history 中的 `--password`。

## 平台状态

systemd、OpenRC、SysVinit 和 runit Adapter 已实现；已识别的 Debian、Ubuntu、RHEL-like、Alpine、Arch、Gentoo、Void 等发行版标记为 `targeted`，不宣称 `verified`。只有在特定发行版版本上完成安装到卸载 E2E 后才能提升为 verified。

CI 门禁运行 `go test ./...`、race、vet、Staticcheck、Govulncheck、静态 Linux 构建、POSIX `sh -n`、ShellCheck，并增加 Alpine 3.22 构建测试。真实 OpenRC/systemd 安装、低端口、更新回滚和卸载 E2E 仍属于发布资格验证。

实现细节见 [DEVELOPMENT.md](DEVELOPMENT.md) 和 [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md)。
