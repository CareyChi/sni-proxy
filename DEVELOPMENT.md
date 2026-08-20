# SNI Proxy 开发文档

## 功能状态图例

| 图标 | 含义 |
|---|---|
| ☑ | 尚未实现 |
| ☑ | 已完整实现 |
| 🟡 | 已实现，但存在已知缺口或尚未达到目标质量 |
| ❗ | 即将过时，后续版本计划替换 |
| ☒ | 已移除，不再提供 |

> 本文档是随源码同步维护的开发清单，不是产品介绍。状态只按仓库中的真实实现填写；新增、重构或移除功能时，必须同步更新本文件。参数标记为“是”的字段为必填项；未列出输入表示该功能不接收用户输入。

## 统一数据约定

| 类型 | 定义 |
|---|---|
| `string` | UTF-8 字符串 |
| `integer` | 十进制整数 |
| `boolean` | `true` 或 `false` |
| `enum<T>` | 只能取列出的字符串值之一 |
| `object<T>` | 名为 `T` 的 JSON 对象 |
| `array<T>` | `T` 类型的 JSON 数组 |
| `duration` | Go `time.Duration` 字符串，如 `5s` |
| `path` | Linux 绝对路径字符串 |

统一 API 响应为 `{"data": ..., "error": null}`；失败响应的 `error` 为 `{"code": string, "message": string}`。密码、私钥和令牌不得出现在系统信息、安装元数据或日志中。

# WebUI

## 身份认证模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 管理员登录 | `username`、`password` | `string`、`string` | 是、是 | `POST /api/v1/auth/login` 校验凭据哈希；成功后签发 `HttpOnly`、`SameSite=Strict` 会话 Cookie，前端不持久化明文密码。 |
| ☑ | 会话状态 | 无 | `object<AuthSession>` | — | `GET /api/v1/auth/session` 返回当前用户名、登录状态和过期时间；未登录统一返回 401。 |
| ☑ | 安全退出 | 无 | `boolean` | — | `POST /api/v1/auth/logout` 删除服务端会话并让 Cookie 立即过期。 |

## 总览模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 运行状态总览 | 无 | `object<RuntimeSummary>` | — | `GET /api/v1/status` 聚合核心服务、Web、开机自启、版本和端口状态；界面按正常、异常、未知三态渲染。 |
| ☑ | 健康状态刷新 | 无 | `object<HealthStatus>` | — | `GET /api/v1/health` 执行进程内健康检查；前端支持手动刷新并进行低频轮询。 |
| ☑ | 后台访问地址 | 无 | `array<string>` | — | 系统信息 API 使用 Go 网络接口与 UDP 路由探测生成可访问地址，不依赖 `ip`、`ss` 或 `ifconfig`。 |

## 服务管理模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 启动应用 | 无 | `object<ServiceState>` | — | `POST /api/v1/service/start` 调用统一 `ServiceManager.Start`，不得在 Handler 内执行 `systemctl`。 |
| 🟡 | 停止应用 | 无 | `object<ServiceState>` | — | `POST /api/v1/service/stop` 调用统一 `ServiceManager.Stop`；Web 进程与被管理核心进程分离时才开放。 |
| 🟡 | 重启应用 | 无 | `object<ServiceState>` | — | `POST /api/v1/service/restart` 调用当前 Init Adapter，并在完成后执行健康检查。 |
| ☑ | 开启开机自启 | 无 | `object<ServiceState>` | — | `POST /api/v1/service/enable` 调用 `ServiceManager.Enable`，再用 `IsEnabled` 复核真实状态。 |
| ☑ | 关闭开机自启 | 无 | `object<ServiceState>` | — | `POST /api/v1/service/disable` 调用 `ServiceManager.Disable`，不得伪造成功。 |

## 端口与网络模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 查看监听端口 | 无 | `object<PortConfig>` | — | `GET /api/v1/config/ports` 从统一配置存储读取 HTTP、HTTPS、Web 端口。 |
| ☑ | 检查端口可用性 | `port`、`host` | `integer`、`string` | 是、否 | `POST /api/v1/network/check-port` 由 Go `net.Listen` 试绑定，端口限定 1–65535；不调用 `ss`、`netstat` 或 `lsof`。 |
| 🟡 | 更换端口 | `http_port`、`https_port`、`web_port` | `integer`、`integer`、`integer` | 是、是、是 | `PUT /api/v1/config/ports` 先校验范围、互异性与占用，再原子写配置并通过统一服务管理器重启；失败回滚旧配置。 |

## 凭据管理模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 查看当前用户名 | 无 | `string` | — | `GET /api/v1/admin/credentials` 只返回用户名，绝不返回当前密码或哈希。 |
| ☑ | 重置用户名和密码 | `username`、`password` | `string`、`string` | 是、否 | `PUT /api/v1/admin/credentials`；用户名按规则校验，密码缺省时用加密安全随机源生成 10 位密码，哈希后原子保存，明文只在本次响应展示一次。 |

## 更新模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 检测更新 | `channel` | `enum<stable,beta>` | 否 | `POST /api/v1/update/check` 由 Go HTTP 客户端访问 GitHub API、使用 `encoding/json` 解析并校验版本，不依赖 Shell、`curl` 或 `jq`。 |
| 🟡 | 执行更新 | `version` | `string` | 是 | `POST /api/v1/update/apply` 下载并验证完整 linux-amd64 包，更新 `/opt/sni-proxy` 内程序、脚本和模板，同步服务注册后调用统一 Adapter 重启；不在当前源码阶段执行。 |

## 系统信息模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | Linux 平台信息 | 无 | `object<PlatformInfo>` | — | `GET /api/v1/system/info` 返回 OS、发行版名称/ID/版本、内核、规范化架构、Init、服务管理器、包管理器及实际安装目录。 |
| ☑ | 安装目录展示 | 无 | `path` | — | 后端从可执行文件真实路径、安装元数据和安全默认值确定 `InstallDir`；WebUI 显示 API 的真实值，不写死 `/opt/sni-proxy`。 |
| ☑ | 发行版兼容模式提示 | 无 | `enum<verified,generic>` | — | 已列发行版显示已支持；未知发行版在能力满足时显示 Generic Linux，不宣称已验证。 |

# 后端

## HTTP API 与 Web 资源

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | HTTP 服务启动 | `listen`、`config` | `string`、`path` | 否、否 | `sni-proxy serve` 使用 Go 标准库启动 API、WebUI 和健康端点；默认 Web 端口 6866。 |
| ☑ | API 认证中间件 | `session_cookie` | `string` | 是 | 除登录与健康检查外，所有管理 API 均校验服务端会话、来源与方法；状态变更只允许 JSON 请求。 |
| ☑ | WebUI 静态资源内嵌 | 无 | `fs.FS` | — | 使用 `go:embed` 将 HTML/CSS/JS 打进静态 Go 二进制，服务器无需 Node.js。 |
| ☑ | 结构化错误处理 | `error` | `error` | 是 | 将校验、冲突、平台不支持和内部错误映射为稳定错误码与恰当 HTTP 状态，不向客户端泄漏敏感路径或命令输出。 |

## 配置与路径模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 统一路径默认值 | `install_dir`、`config_dir`、`data_dir`、`log_dir` | `path` × 4 | 否 | 单一 `Paths` 模型默认 `/opt/sni-proxy`、`/etc/sni-proxy`、`/var/lib/sni-proxy`、`/var/log/sni-proxy`，其他模块只引用该模型。 |
| ☑ | 实际安装目录解析 | `executable_path` | `path` | 否 | Go 使用 `os.Executable` 与 `filepath.EvalSymlinks`；Shell 使用 POSIX 循环逐层解析 Symlink，再由 `bin/..` 推导根目录。 |
| ☑ | 配置读取 | `config_path` | `path` | 否 | `sni-proxy config get` 读取 JSON 配置并应用安全默认值，严格校验端口、路径与权限。 |
| ☑ | 配置原子写入 | `config` | `object<Config>` | 是 | 在同目录创建权限为 0600 的临时文件、写入并 `fsync` 后重命名，避免中断产生半文件。 |
| ☑ | 安装元数据读写 | `metadata` | `object<InstallMetadata>` | 是 | `/opt/sni-proxy/install-meta.json` 只保存目录、发行版、版本、Init 和架构；禁止凭据、私钥、Token。 |

## 平台与发行版检测模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | os-release 安全解析 | `reader` | `io.Reader` | 是 | 只解析 `ID`、`ID_LIKE`、`NAME`、`PRETTY_NAME`、`VERSION`、`VERSION_ID`，处理规范引号和转义，不 source 文件。 |
| ☑ | 发行版检测 | `root` | `path` | 否 | 优先 `/etc/os-release`、其次 `/usr/lib/os-release`，再检查 lsb/Debian/Red Hat/Alpine/Arch/SUSE 标记文件。 |
| ☑ | 发行版族映射 | `id`、`id_like` | `string`、`array<string>` | 是、否 | 映射 Debian、Red Hat、SUSE、Arch、Alpine 等族；未知系统保留原始信息并进入能力检测。 |
| ☑ | 架构检测与规范化 | `GOARCH` 或 `uname_machine` | `string` | 是 | 接受 `x86_64`/`amd64` 并规范为 `amd64`；arm64、armv7、386 等当前阶段返回明确不支持错误。 |
| ☑ | 内核检测 | 无 | `string` | — | 优先 Go/系统调用获得 Linux 内核版本，失败时返回 `unknown` 而不影响其他平台字段。 |
| ☑ | 容器环境检测 | 无 | `object<ContainerInfo>` | — | 综合 `/.dockerenv`、`/run/.containerenv`、cgroup 与环境提示识别 Docker、Podman、LXC、OpenVZ；容器本身不等于不支持。 |
| ☑ | 包管理器检测 | `PATH` | `enum<apt,dnf,yum,apk,zypper,pacman,emerge,xbps,unknown>` | 是 | 按实际可执行文件能力检测，发行版仅用于优先级消歧；Shell 暴露 `pkg_update/pkg_install/pkg_is_installed`。 |
| ☑ | Init System 检测 | `root`、`PATH` | `enum<systemd,openrc,sysvinit,runit,unknown>` | 否、是 | 结合运行目录、管理命令、服务目录和 PID 1 辅助证据；不得仅按发行版或 PID 1 判断。 |

## Service Manager 抽象模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | ServiceManager 工厂 | `platform`、`service_name` | `object<PlatformInfo>`、`string` | 是、是 | 根据真实 Init 返回 systemd/OpenRC/SysVinit/runit Adapter；未知 Init 返回可解释错误，不伪造自启能力。 |
| ☑ | 启动服务 | `context` | `context.Context` | 是 | 统一 `Start(ctx)` 接口，使用参数数组执行固定命令，禁止 `sh -c` 拼接用户输入。 |
| ☑ | 停止服务 | `context` | `context.Context` | 是 | 统一 `Stop(ctx)` 接口并保留真实退出状态。 |
| ☑ | 重启服务 | `context` | `context.Context` | 是 | 统一 `Restart(ctx)` 接口，供 Web、更新与端口修改共同调用。 |
| ☑ | 运行状态 | `context` | `boolean` | 是 | 统一 `IsRunning(ctx)`；退出码表示未运行时返回 `false,nil`，命令缺失或异常返回错误。 |
| ☑ | 开启自启 | `context` | `context.Context` | 是 | 统一 `Enable(ctx)`，由 Adapter 注册到各自标准目录/运行级别。 |
| ☑ | 关闭自启 | `context` | `context.Context` | 是 | 统一 `Disable(ctx)`，不删除 Source of Truth 模板。 |
| ☑ | 自启状态 | `context` | `boolean` | 是 | 统一 `IsEnabled(ctx)`，按各 Init 的真实注册状态判断。 |
| ☑ | 安装服务注册 | `paths` | `object<Paths>` | 是 | 统一 `Install(ctx)` 从 `/opt/sni-proxy/service` 的模板建立系统注册链接；技术上不能链接时复制并记录同步责任。 |
| ☑ | 卸载服务注册 | `paths` | `object<Paths>` | 是 | 统一 `Uninstall(ctx)` 只移除当前 Adapter 的注册入口并执行必要刷新。 |
| ☑ | systemd Adapter | `service_name` | `string` | 是 | 映射 `systemctl start/stop/restart/is-active/enable/disable/is-enabled`，服务注册优先链接 `/etc/systemd/system`。 |
| ☑ | OpenRC Adapter | `service_name` | `string` | 是 | 映射 `rc-service` 与 `rc-update`；完整支持 Alpine/Gentoo，不安装或调用 systemd。 |
| ☑ | SysVinit Adapter | `service_name` | `string` | 是 | 映射 `service` 或 `/etc/init.d`；自启按能力选择 `update-rc.d`、`chkconfig` 或标准 rc 链接。 |
| ☑ | runit Adapter | `service_name`、`service_dir` | `string`、`path` | 是、否 | 映射 `sv up/down/restart/status`，先检测 `/etc/sv`、`/var/service` 等真实目录再注册。 |

## 核心代理与网络模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | SNI/HTTP 代理启动 | `http_port`、`https_port` | `integer`、`integer` | 是、是 | `serve` 在 HTTP 端读取 Host、TLS 端读取 ClientHello SNI，按受控目标策略建立双向 TCP 转发；设置超时、并发限制和优雅关闭。 |
| ☑ | 端口试绑定 | `host`、`port` | `string`、`integer` | 否、是 | `sni-proxy network check-port --port` 使用 Go `net.Listen`，以结构化 JSON 返回可用性和错误类别。 |
| ☑ | 服务器 IP 发现 | 无 | `array<string>` | — | 枚举 `net.Interfaces`，排除 loopback/link-local，并用 UDP route probing 选主地址；无需 iproute2。 |
| ☑ | 健康检查 | `url`、`timeout` | `string`、`duration` | 否、否 | `sni-proxy health` 检查进程内状态和 Web 健康端点，返回机器可读退出码与可选 JSON。 |

## 管理员凭据模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 初始化管理员 | `username`、`password` | `string`、`string` | 否、否 | 首次安装固定默认用户名 `admin`；未传密码时使用 `crypto/rand` 生成 10 位随机密码，保存带随机盐的迭代 KDF 哈希。 |
| ☑ | 校验凭据 | `username`、`password` | `string`、`string` | 是、是 | 常量时间比较派生结果，错误消息不区分用户不存在与密码错误。 |
| ☑ | 重置凭据 CLI | `username`、`password`、`generate` | `string`、`string`、`boolean` | 否、否、否 | `sni-proxy admin reset-credentials` 复用同一存储层；生成的明文密码只输出一次。 |
| ☑ | 凭据镜像 | `credentials` | `object<CredentialRecord>` | 是 | 主存储更新后以最小权限同步非明文镜像；任一写入失败时不报告整体成功。 |

## 更新模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 版本输出 | 无 | `string` | — | `sni-proxy version` 从构建注入值读取，开发态使用明确的 `dev`，安装目录同时维护 `VERSION`。 |
| ☑ | GitHub 更新检查 | `repository`、`channel` | `string`、`enum<stable,beta>` | 是、否 | Go 使用 TLS 校验、超时和 `encoding/json` 请求 Release 元数据并比较语义版本。 |
| ☑ | 更新包校验 | `archive`、`checksum` | `path`、`string` | 是、是 | 校验 SHA-256、包内路径和 linux/amd64 清单，拒绝路径穿越与不完整平台模板。 |
| 🟡 | 原子更新与回滚 | `version` | `string` | 是 | 暂存并备份到 `/var/lib/sni-proxy/backups`，替换 `/opt/sni-proxy` 的唯一正式副本、同步服务注册、重启并健康检查，失败回滚。 |

## Shell 安装兼容层

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 安装环境检测 | `INSTALL_DIR` | `path` | 否 | `install.sh` 按 root→Linux→amd64→发行版/版本→包管理器→Init→系统能力顺序检测并显示真实结果。 |
| ☑ | POSIX/BusyBox 兼容 | 无 | POSIX `sh` | — | 全部运行时脚本使用 `#!/bin/sh`，不使用数组、`[[ ]]`、`BASH_SOURCE`、`readlink -f`、`grep -P`、`sed -r`、`stat --printf`、Python 或 jq。 |
| ☑ | 下载工具与 CA 检测 | 无 | `enum<curl,wget>` | — | 优先 curl、其次 wget；均缺失才用已检测包管理器安装最小工具；验证 CA Bundle，禁止跳过 TLS。 |
| ☑ | 系统用户创建 | `user`、`group` | `string`、`string` | 是、是 | 按命令能力选择 `useradd/groupadd` 或 Alpine `adduser/addgroup`，检测 nologin 路径，创建无登录系统账户。 |
| ☑ | 低端口权限配置 | `binary_path` | `path` | 是 | 检测、应用并验证 `setcap cap_net_bind_service=+ep`；失败时明确停止或要求用户选择显式兼容策略，不静默长期 root 运行。 |
| ☑ | 安装文件布局 | `INSTALL_DIR` | `path` | 否 | 程序、管理脚本、libexec、模板、服务 Source of Truth、VERSION 和元数据全部进入 `/opt/sni-proxy`；配置/数据/日志分别进入 Linux 标准目录。 |
| ☑ | 外部命令链接 | `target`、`link` | `path`、`path` | 是、是 | `/usr/local/bin/sni-proxy{,ctl}` 只创建指向安装目录的符号链接，不复制第二份源码。 |
| ☑ | 服务安装与启动 | `init_system` | `enum<systemd,openrc,sysvinit,runit>` | 是 | 选择对应模板注册、开启自启、启动并执行真实健康检查；未知 Init 明确提示只能手动运行。 |

## 管理脚本模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | Symlink 安全解析 | `argv0` | `path` | 是 | `sni-proxyctl` 用 POSIX 循环处理绝对/相对多层 Symlink，限制跳转次数，解析真实 `bin` 后计算实际 `INSTALL_DIR`。 |
| ☑ | 顶部状态面板 | 无 | 文本 | — | 强制显示实际安装目录，并显示发行版/版本/Init、核心、Web、自启、版本和三个端口；状态来自 Go CLI 与统一 Service API。 |
| ☑ | 动态管理菜单 | `choice` | `integer` | 是 | 根据运行和自启状态显示检测更新、后台信息、启动/重启/停止、更换端口、重置凭据、启停自启、卸载；菜单不直接出现 Init 专用命令。 |
| ☑ | 获取后台信息 | 无 | `object<SystemInfo>` | — | 调用 `sni-proxy info --json` 获取真实 IP、端口和平台信息，Shell 只负责可读展示。 |
| ☑ | 管理更新 | 无 | `object<UpdateInfo>` | — | 调用 Go `update check/apply`，更新后由统一 Service Manager 重启。 |

## 服务模板模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | systemd 服务模板 | 路径/用户占位符 | `path`、`string` | 是 | Source of Truth 位于 `/opt/sni-proxy/service/systemd/sni-proxy.service`，以前台方式启动并配置重启、目录和最小权限。 |
| ☑ | OpenRC 服务模板 | 路径/用户占位符 | `path`、`string` | 是 | 使用 `supervise-daemon`、`command_args`、`command_user`、`pidfile`、`directory`、`stop_timeout`；依赖 `need net`，避免不存在的强制服务。 |
| ☑ | SysVinit 服务模板 | 路径/用户占位符 | `path`、`string` | 是 | POSIX init 脚本实现 start/stop/restart/status，使用可靠 start-stop-daemon 或检测到的标准工具管理 PID。 |
| ☑ | runit 服务模板 | 路径/用户占位符 | `path`、`string` | 是 | `run` 使用 `exec` 前台运行并降权，注册位置由检测结果决定。 |

## 安全卸载模块

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | 实际目录确认 | `install_meta`、`resolved_script_dir` | `path`、`path` | 否、是 | 优先交叉验证元数据与脚本真实目录；要求非空、绝对路径、不是 `/` 且含安装标记。 |
| ☑ | 跨 Init 停止与注销 | `ServiceManager` | `object<ServiceManager>` | 是 | 依次 Stop、Disable、Uninstall；只调用当前 Adapter，Alpine 不执行 systemctl。 |
| ☑ | 完全卸载 | `confirm`、`purge_data` | `string`、`boolean` | 是、否 | 明确确认后删除两个外部 Symlink、实际安装目录和配置；仅在选择清除数据时删除数据/日志，所有递归目标先经过安全校验。 |

## 测试源码（本阶段只编写，不执行）

| 状态 | 功能名字 | 可输入参数 | 目标数据类型 | 必填 | 具体实现方式 |
|---|---|---|---|---|---|
| ☑ | os-release 解析表测试 | `fixture` | `string` | 是 | 覆盖引号、转义、未知 Key、恶意命令文本和缺失字段。 |
| ☑ | 发行版检测表测试 | `root fixture` | `path` | 是 | 覆盖 Debian、Ubuntu、RHEL-like、Alpine、Arch、SUSE 与 Generic Linux。 |
| ☑ | Init 检测表测试 | `capabilities` | `object<FakeSystem>` | 是 | 覆盖 systemd、OpenRC、runit、SysVinit、冲突证据和未知 Init。 |
| ☑ | 包管理器检测测试 | `PATH fixture` | `path` | 是 | 覆盖 apt-get、dnf、yum、apk、zypper、pacman、emerge、xbps-install。 |
| ☑ | Service 命令测试 | `adapter`、`operation` | `enum`、`enum` | 是、是 | 使用假执行器断言四类 Adapter 的参数数组、退出码解释和禁止命令注入。 |
| ☑ | 安装目录与 Symlink 测试 | `link graph` | `object` | 是 | 覆盖绝对、相对、多层、断链和循环 Symlink；Shell 另提供可在 Linux/BusyBox 运行的测试脚本。 |
| 🟡 | Alpine/OpenRC 场景测试 | `fixture` | `object` | 是 | 覆盖安装、启停、重启、状态、自启、关闭自启、端口、Web、更新和卸载命令路径。 |
| 🟡 | 卸载路径安全测试 | `install_dir` | `path` | 是 | 覆盖空值、`/`、非安装目录、缺少标记、合法自定义安装目录，确保危险目标被拒绝。 |

## 黄圈功能的已知缺口

- WebUI 与核心代理目前由同一个服务进程承载。通过 Web 发起停止或重启时，底层 Adapter 会执行正确命令，但 HTTP 响应可能因当前进程被停止而提前断开；管理脚本与独立 CLI 不受此限制。
- Web 修改端口已实现校验、原子保存和失败前回写，但自托管进程在重启期间无法向原连接确认新实例健康。管理脚本路径具备显式端口回滚。
- 更新器已实现 TLS、SHA-256、路径安全、完整资源清单、实际目录模板渲染、原子目录切换和备份；独立 CLI 更新会通过当前 Adapter 重启并等待健康，但健康失败后只保留回滚副本，尚未自动二次切回。Web 更新采用响应后重启，因此也无法在原请求内完成跨 Init 的重启后回滚确认。
- Alpine/OpenRC 与卸载安全目前有表驱动/静态测试源码，但按本阶段要求没有执行，也尚未加入真实 Alpine 虚拟机/容器的端到端测试。因此不能标记为完美实现。

## 未来发布前验证门禁

当前阶段不得执行编译或测试。进入编译验证阶段后，必须至少在 `Alpine Linux amd64 + OpenRC + BusyBox + musl` 和一个 `systemd` 发行版中验证安装、启动、停止、重启、状态、自启/关闭自启、Web 后台、80/443/6866 监听、更新与卸载；最终二进制目标为 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`。
