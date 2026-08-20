# SNI Proxy 项目结构

## 设计边界

项目将“程序与脚本”“配置”“持久数据”“日志”分开：

| 类别 | 默认目录 | 内容与原因 |
|---|---|---|
| Program / Script | `/opt/sni-proxy` | 核心程序、管理脚本、Shell 模块、服务 Source of Truth、模板、版本和非敏感安装元数据。所有可更新程序内容集中在一个根目录。 |
| Configuration | `/etc/sni-proxy` | root-owned 运行配置与发布公钥信任策略，服务用户只读。 |
| Persistent Data | `/var/lib/sni-proxy` | Argon2id 凭据记录、备份和其他持久状态。 |
| Logs | `/var/log/sni-proxy` | 服务日志，避免程序目录随更新或卸载策略混淆。 |

`internal/config.Paths` 是 Go 侧唯一默认值来源；Shell 使用 `common.sh` 的同组统一默认变量。默认安装根为 `/opt/sni-proxy`，但实际目录通过可执行文件/Symlink 与 `install-meta.json` 解析，不能为了显示而写死。

## 开发仓库

```text
.
├── cmd/sni-proxy/main.go          # 单一 CLI/服务入口
├── internal/
│   ├── config/                    # 路径、运行配置、安装元数据与原子写入
│   ├── credentials/               # Argon2id 管理员记录与随机密码
│   ├── network/                   # 端口试绑定、IP 发现、安全上游解析
│   ├── platform/                  # 发行版、架构、包管理器、Init、容器检测
│   ├── proxy/                     # HTTP Host 与 TLS SNI 转发
│   ├── service/                   # ServiceManager 及四套 Adapter
│   ├── update/                    # SemVer、Ed25519 manifest、capability 与事务回滚
│   └── web/                       # HTTP API、认证与内嵌 WebUI
├── scripts/
│   ├── install.sh                 # POSIX/BusyBox 安装入口
│   ├── sni-proxyctl               # 解析真实目录的动态管理菜单
│   ├── assemble-release.sh        # 只打包已有二进制，不负责编译
│   ├── lib/                       # Shell 统一兼容层及 Adapter
│   └── tests/                     # 未来 Linux/BusyBox Shell 测试源码
├── service/
│   ├── systemd/
│   ├── openrc/
│   ├── sysvinit/
│   └── runit/
├── templates/
├── DEVELOPMENT.md
├── README.md
├── VERSION
└── go.mod
```

## 安装后结构

```text
/opt/sni-proxy/
├── bin/
│   ├── sni-proxy
│   └── sni-proxyctl
├── libexec/
│   ├── install.sh
│   ├── common.sh
│   ├── distro.sh
│   ├── service.sh
│   ├── systemd.sh
│   ├── openrc.sh
│   ├── sysv.sh
│   ├── runit.sh
│   ├── network.sh
│   ├── update.sh
│   └── uninstall.sh
├── service/
│   ├── systemd/sni-proxy.service
│   ├── openrc/sni-proxy
│   ├── sysvinit/sni-proxy
│   └── runit/sni-proxy/run
├── templates/
├── VERSION
└── install-meta.json
```

`/usr/local/bin/sni-proxy{,ctl}` 只允许是指向 `/opt/sni-proxy/bin` 的符号链接。服务系统规定的注册目录可以放置链接；若目标 Init 技术上不能可靠使用链接，安装层可以复制，但 `/opt/sni-proxy/service` 永远是 Source of Truth，更新时必须同步。

## Linux 平台兼容层

### Distribution Detection

Go 的 `platform.Detector` 与 Shell 的 `distro.sh` 均按 `/etc/os-release`、`/usr/lib/os-release`、发行版标记文件的顺序读取。只解析 ID、ID_LIKE、NAME、PRETTY_NAME、VERSION、VERSION_ID；Shell 不 source `os-release`，Go 解析器也忽略未知 Key，因此文件内容不会作为命令执行。

### Init Detection

Init 与发行版名称解耦。检测组合运行目录、管理命令、服务目录和 `/proc/1/comm` 辅助证据：

- systemd：`/run/systemd/system` 与 `systemctl`。
- OpenRC：`rc-service` 与 `rc-update`。
- runit：`sv` 以及 `runsvdir` 或实际服务目录。
- SysVinit：`service`/PID 1 辅助证据与 `/etc/init.d`。

检测不到受支持的 Init 时，核心程序仍可用 `sni-proxy serve` 手动运行，但安装器会停止自动服务注册并明确说明不能提供开机自启。

### Package Manager Detection

按实际可执行能力检测 `apt-get`、`dnf`、`yum`、`apk`、`zypper`、`pacman`、`emerge` 与 `xbps-install`，发行版信息只用于消歧。Shell 统一暴露 `pkg_update`、`pkg_install`、`pkg_is_installed`，只在缺少下载器、CA 或 File Capability 工具时安装最小必要包。

### Service Manager Interface

Go `internal/service.Manager` 和 Shell `service_*` 均提供：

```text
Start / service_start
Stop / service_stop
Restart / service_restart
IsRunning / service_is_running
Enable / service_enable
Disable / service_disable
IsEnabled / service_is_enabled
Install / service_install
Uninstall / service_uninstall
```

Web Handler 只查询此接口；高权限服务、端口和更新写操作只由 root CLI 调用，不在网络进程中开放。

### Systemd Adapter

使用 `systemctl` 的 start/stop/restart/is-active/enable/disable/is-enabled。注册入口优先链接 `/etc/systemd/system/sni-proxy.service` 到安装目录的模板，并执行 daemon-reload。Unit 以 `sni-proxy` 用户运行，将 Capability Bounding Set 限制为低端口能力，并只给配置、数据、日志目录写权限。

### OpenRC Adapter

使用 `rc-service` 与 `rc-update`，完整实现启停、重启、状态、自启和关闭自启。模板采用 `supervise-daemon`、服务用户、PID 文件、工作目录、日志和 stop timeout；依赖仅声明 `need net`，适合 Alpine 与使用 OpenRC 的 Gentoo 等系统。

### SysVinit Adapter

服务脚本使用系统 `start-stop-daemon` 可靠管理前台 Go 进程与 PID 文件，没有 `command &; echo $!`。自启按能力选择 `update-rc.d`、`chkconfig` 或标准 rc2–rc5 符号链接。

### Runit Adapter

先检测 `/var/service`、`/run/service`、`/etc/service`，再用 `sv up/down/restart/status`。`run` 脚本通过 runit 常见的 `chpst` 或 `setuidgid` 降权并 `exec` 前台进程，不假设所有系统目录相同。

### BusyBox Compatibility

运行时脚本统一 `#!/bin/sh`，不使用 Bash 数组、`[[ ]]`、`mapfile`、`BASH_SOURCE`、Process Substitution、`eval`、`grep -P`、`sed -r`、`readlink -f`、`stat --printf`、Python 或 jq。符号链接通过 `readlink` 单步循环解析，并用物理目录规范化；复杂 JSON、网络接口、端口和 Release 逻辑放入 Go。

### Low Port Privilege

四套 Init 都以 `sni-proxy` 系统用户运行。安装器检测并按包管理器安装最小 libcap 工具，执行 `setcap cap_net_bind_service=+ep`，再通过 `getcap` 验证。工具缺失、文件系统不支持或验证失败时安装会明确停止，不会静默让完整 Web/代理长期以 root 运行。

### Container Handling

Docker、Podman、LXC、OpenVZ 或 containerd 只作为平台信息，不直接判定 Unsupported。只要端口、权限和受支持 Init 能力存在即可工作；没有传统 Init 时明确降级为手动 `serve`，不伪造自启。

## 发行版支持矩阵

“源码已实现”表示仓库中存在对应检测与 Adapter；单元测试和容器构建不能等同于已经完成目标机安装到卸载实测。

| Distribution | Package Manager | Default Init | Source Support | Runtime Validation |
|---|---|---|---|---|
| Debian | apt | systemd | Full adapter source | Pending |
| Ubuntu | apt | systemd | Full adapter source | Pending |
| Linux Mint / Pop!_OS / Kali | apt | systemd | Full adapter source | Pending |
| RHEL / Rocky / Alma / CentOS Stream / Oracle | dnf/yum | systemd | Full adapter source | Pending |
| Fedora | dnf | systemd | Full adapter source | Pending |
| openSUSE / SLES | zypper | systemd | Full adapter source | Pending |
| Arch / Manjaro / EndeavourOS | pacman | systemd | Full adapter source | Pending |
| Alpine Linux | apk | OpenRC | Full adapter source | Pending mandatory gate |
| Gentoo | emerge | OpenRC/systemd | Capability-selected full adapters | Pending |
| Devuan | apt | SysVinit/OpenRC | Capability-selected full adapters | Pending |
| Void Linux | xbps | runit | Full adapter source | Pending |
| Unknown Linux | detected executable | detected supported Init | Generic Linux capability mode | Not claimed verified |

## WebUI 与 API

`internal/web/assets` 通过 `go:embed` 进入二进制，分为认证、总览、服务管理、端口与网络、管理员凭据、版本更新、系统信息模块。API 的平台对象包含：

```json
{
  "os": "linux",
  "distribution": "Alpine Linux",
  "distribution_id": "alpine",
  "distribution_version": "3.xx",
  "architecture": "amd64",
  "kernel": "6.x",
  "init_system": "openrc",
  "service_manager": "openrc",
  "install_dir": "/opt/sni-proxy"
}
```

服务状态查询走 `service.Manager`；Web 服务、端口与更新写操作固定返回禁止。端口由 Go `net.Listen` 试绑定；IP 使用 `net.Interfaces` 与 UDP route probing；GitHub Release 使用 Go TLS/HTTP 与 `encoding/json`。服务器运行时不需要 Node.js、Python、jq、Go 或 Java。

## 更新与卸载

单一 linux-amd64 更新包包含核心程序、管理脚本、全部 libexec 和 systemd/OpenRC/SysVinit/runit 模板。Go 更新器先用 root-owned Ed25519 公钥验证覆盖整个 archive 的 manifest，再验证 SHA-256、解压路径、文件类型、ELF 架构、owner/mode 与低端口 capability。目录切换、服务注册、重启和三个端口 readiness 属于同一事务；失败会恢复旧目录、旧注册并健康检查旧版本。

卸载从管理脚本的真实路径与安装 Metadata 获取目标，拒绝非 canonical、越出角色允许空间、互相重叠或 marker/installation ID/owner/mode 不匹配的目录。随后只调用当前 Init Adapter 停止、关闭自启和注销，删除两个外部 Symlink，再删除四个经过验证的目录。

## 测试源码与未来验证

仓库测试覆盖 os-release、平台/Init、Adapter、Argon2id、deny-by-default 域名策略、special-use IP、HTTP 策略、TLS 分片/模糊测试、buffered-reader idle timeout、登录限制、签名 manifest、恶意 archive、SemVer 和回滚恢复。CI 执行 test/race/vet、Staticcheck、Govulncheck、静态 Linux 构建、ShellCheck 与 Alpine 构建；仍需增加真实 Alpine/OpenRC 与 systemd 安装到卸载 E2E。
