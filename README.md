# SNI Proxy

SNI Proxy 是一个面向 Linux amd64 的静态 Go 代理与 Web 管理后台。它同时提供 HTTP Host 转发、TLS ClientHello SNI 转发、管理员控制台和跨 Init System 的服务管理层。

> 当前仓库阶段仅提供源码：不包含编译产物、Linux 二进制、GitHub Release，也没有在本阶段执行编译或测试。安装脚本会明确要求已有的 `bin/sni-proxy`，不会在服务器上安装 Go 或 Node.js 后现场编译。

## 主要能力

- 统一安装根目录 `/opt/sni-proxy`，程序、管理脚本、Shell 模块和服务模板只有一份正式副本。
- `/usr/local/bin/sni-proxy` 与 `/usr/local/bin/sni-proxyctl` 只是指向安装目录的符号链接。
- POSIX `sh` 管理脚本，不使用 Bash 数组、`[[ ]]`、`readlink -f`、Python 或 jq。
- 独立检测 Linux 发行版、版本、架构、包管理器、Init System 与容器环境。
- 同一 Service Manager API 支持 systemd、OpenRC、SysVinit 和 runit。
- 内嵌 WebUI，无需服务器安装 Node.js；端口检查、IP 发现和 GitHub JSON 解析由 Go 完成。
- 默认拒绝把代理目标解析到私有、回环、链路本地等非公网地址，可通过受控配置显式调整。
- 管理员凭据使用随机盐与迭代哈希；自动生成的 10 位密码只显示一次。

## 默认目录

| 用途 | 默认路径 |
|---|---|
| 程序、脚本和模板 | `/opt/sni-proxy` |
| 配置 | `/etc/sni-proxy` |
| 持久数据与备份 | `/var/lib/sni-proxy` |
| 日志 | `/var/log/sni-proxy` |

安装后目录：

```text
/opt/sni-proxy/
├── bin/
│   ├── sni-proxy
│   └── sni-proxyctl
├── libexec/
├── service/
│   ├── systemd/
│   ├── openrc/
│   ├── sysvinit/
│   └── runit/
├── templates/
├── VERSION
└── install-meta.json
```

程序目录不保存业务配置、数据库或日志。这样既保证所有脚本与运行依赖集中更新，也遵守 Linux 对配置、持久数据和日志的目录约定。

## 源码阶段安装入口

未来先将静态 `linux/amd64` 二进制放在仓库的 `bin/sni-proxy`，再以 root 运行：

```sh
sudo ./scripts/install.sh
```

安装顺序为 root/Linux/amd64 检查、发行版与包管理器检测、Init 检测、系统用户创建、统一目录部署、管理员初始化、服务注册、低端口 File Capability、自启、启动和真实健康检查。未知发行版可在能力满足时进入 Generic Linux 模式；未知 Init 不会伪造服务或自启成功。

安装完成后统一使用：

```sh
sni-proxyctl
```

管理脚本通过多层符号链接解析自己的真实路径，顶部强制显示实际安装目录，并提供更新检查、后台信息、启停/重启、端口修改、凭据重置、自启切换和完全卸载。

## Alpine Linux

Alpine Linux amd64 是一级支持目标：

- 使用原生 OpenRC，不安装也不调用 systemd。
- 管理脚本兼容 BusyBox `ash` userland。
- OpenRC 服务由 `supervise-daemon` 管理，声明 `need net`，不依赖一个并非处处存在的防火墙服务名。
- 服务用户使用 Alpine 的 `addgroup -S` / `adduser -S` 能力分支创建。
- 最终核心程序目标为 `CGO_ENABLED=0`，不依赖 glibc，可运行于 musl 环境。
- 默认 HTTP 80、HTTPS 443、Web 6866；低端口通过已验证的 `CAP_NET_BIND_SERVICE` File Capability 提供。

未来发布验证必须覆盖 Alpine amd64、OpenRC、BusyBox 与 musl 下的安装、启动、停止、重启、状态、自启/关闭自启、Web、更新和卸载全流程。

## 手动服务命令

优先使用 `sni-proxyctl`。排障时可按实际 Init 使用：

| Init | 启动 | 停止 | 重启 | 自启 |
|---|---|---|---|---|
| systemd | `systemctl start sni-proxy` | `systemctl stop sni-proxy` | `systemctl restart sni-proxy` | `systemctl enable sni-proxy` |
| OpenRC | `rc-service sni-proxy start` | `rc-service sni-proxy stop` | `rc-service sni-proxy restart` | `rc-update add sni-proxy default` |
| SysVinit | `service sni-proxy start` | `service sni-proxy stop` | `service sni-proxy restart` | `update-rc.d`、`chkconfig` 或标准 rc 链接 |
| runit | `sv up /var/service/sni-proxy` | `sv down /var/service/sni-proxy` | `sv restart /var/service/sni-proxy` | 注册到检测到的 runsvdir 服务目录 |

Go CLI 同样提供统一入口：

```sh
sni-proxy service restart
sni-proxy service is-running
sni-proxy platform --json
sni-proxy info --json
sni-proxy network check-port --port 6866 --json
```

## 开发文档

功能状态、输入参数、目标类型和具体实现位于 [DEVELOPMENT.md](DEVELOPMENT.md)。源码布局与平台适配细节位于 [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md)。

## 安全边界

- Release 下载必须通过正常 TLS 证书校验，并校验 SHA-256；禁止 `curl -k` 或 `wget --no-check-certificate`。
- 安装元数据不保存密码、凭据私钥或 Token。
- 完全卸载会交叉检查真实脚本目录、绝对路径与安装标记，拒绝空路径、`/` 和系统根目录。
- Web 管理变更要求认证会话、同源 JSON 请求；Cookie 使用 HttpOnly 与 SameSite Strict。
- 当前只接受 `amd64`/`x86_64`。其他架构会明确拒绝，而不是下载并强行运行 amd64 二进制。
