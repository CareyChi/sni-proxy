# SNI Proxy 开发说明

本文记录当前安全边界、事务语义和发布门禁。实现变更必须同步更新这里与测试。

## 进程与权限边界

`sni-proxy serve` 始终以 `sni-proxy` 普通用户运行。它负责 HTTP/SNI 代理、Web 静态资源、登录、只读状态查询和凭据重置，不拥有服务注册、安装目录替换或 root 配置写权限。

Web 固定禁止以下写操作并返回 `403 privileged_operation_disabled`：

- `/api/v1/service/{start,stop,restart,enable,disable}`
- `PUT /api/v1/config/ports`
- `POST /api/v1/update/apply`

高权限操作只允许 root CLI：`sni-proxy service ...`、`config set-ports`、`update apply`。未来如果恢复 Web 写操作，必须先实现 root-owned Unix socket helper、固定枚举 RPC、socket owner/group 权限、Linux peer credential 校验和 helper 内部参数复核；不得增加通用命令执行 RPC 或大范围 NOPASSWD sudo。

## 网络入口

默认配置：

```json
{
  "proxy_listen_address": "0.0.0.0",
  "admin_listen_address": "127.0.0.1",
  "http_port": 80,
  "https_port": 443,
  "web_port": 6866,
  "allowed_domains": [],
  "max_connections": 1024,
  "max_connections_per_ip": 32
}
```

空 `allowed_domains` 表示拒绝全部目标。HTTP 使用 `http.Server` 与 `httputil.ReverseProxy`，每个 request 独立校验 Host，只允许目标端口 80，并拒绝 CONNECT；标准库负责 header 上限、重复 Host、keep-alive 和 Transfer-Encoding 语义。TLS 443 保持 L4 tunnel，只解析 ClientHello SNI。

TLS parser 由 bounded cursor 读取 U8/U16/U24/bytes，受 128 KiB preface、TLS record 长度和 ClientHello declared length 三层限制，不设 record-count 上限。测试必须维持多 record、逐字节分片、错误 list length、零长度/重复 host_name 和 fuzz 不 panic 覆盖。

Tunnel 显式接收底层 `srcConn` 与可能带预读数据的 `io.Reader`，每次 read/write 前刷新对应 deadline；非 EOF 错误会同时打断另一方向。全局和每 IP limiter 同时覆盖 HTTP/TLS listener。

## 出站策略

`internal/network` 使用 `net/netip`。DNS 与所有地址连接尝试共享一个 `context.WithTimeout` 总预算；每个实际 dial IP 在构造 socket 地址前重新检查策略。IPv6/IPv4 地址交错并以受控延迟并行，首个成功连接会取消其余尝试并关闭迟到连接。

默认 deny table 包括 RFC1918、CGNAT、loopback、link-local、benchmark、documentation、multicast、reserved、IPv4-mapped private IPv6、IPv6 documentation/ULA 等 special-use prefix。新增或调整 prefix 必须更新 table-driven tests。

## 管理认证

新凭据记录为 version 2 Argon2id，保存 algorithm、version、memory、time、parallelism、salt 和 hash。当前参数是 64 MiB、time=3、parallelism=2、32-byte hash；读取端对参数设置上限，避免恶意记录触发资源耗尽。version 1 自定义 SHA-256 KDF 仅保留验证兼容，下一次重置会写成 Argon2id。

登录入口有每 RemoteAddr IP token bucket、指数失败退避、最多 4 个并发密码验证和最多 64 个服务端会话。不会信任 X-Forwarded-For。Cookie 的 Secure 只由显式 `cookie_secure` 决定；`admin_public_url=https://...` 时校验要求 Secure=true，不读取 X-Forwarded-Proto。

CLI 默认在 TTY 隐藏读取并二次确认密码。自动化使用 `--password-file`、`--password-stdin` 或 `--generate`；`--password` 仅兼容保留并打印 argv/history 风险警告。凭据 mirror 已移除，避免半事务双写。

## 路径与卸载

允许空间固定为：install 位于 `/opt/*`、`/srv/*` 或 `/usr/local/lib/*`；config 位于 `/etc/*`；data 位于 `/var/lib/*`；log 位于 `/var/log/*`。输入必须绝对、已经 Clean、无 `.`/`..`/重复分隔符/尾分隔符，并解析已存在父目录和 symlink 后仍与输入完全一致。四个目录必须不相等且不互相包含。

安装生成 128-bit 随机 installation ID，写入 root-owned `install-meta.json` 和四个 `.sni-proxy-managed` marker。卸载前再次验证 canonical path、允许空间、目录互斥、marker role/ID、root owner 和 mode；任一步失败都不得递归删除。

`/etc/sni-proxy` 为 root-owned、daemon 只读；`/var/lib/sni-proxy` 和 `/var/log/sni-proxy` 才由服务用户拥有。这样运行时漏洞不能替换更新仓库或发布公钥。

## 更新事务与信任链

仓库编译时固定为 `CareyChi/sni-proxy`。`/etc/sni-proxy/update-trust.json` 必须 root-owned 且不可被 group/world 写，包含匹配仓库和 Ed25519 raw public key。Release 必须包含：

- `sni-proxy-linux-amd64.tar.gz`
- `sni-proxy-linux-amd64.manifest.json`
- `sni-proxy-linux-amd64.manifest.json.sig`

签名覆盖 manifest 原始字节；manifest 固定 version、architecture、archive filename 和 archive SHA-256，因此 service templates 与全部 payload 都在签名覆盖的 archive 内。`scripts/assemble-release.sh` 需要 `SNI_PROXY_SIGNING_KEY_FILE`，私钥不得提交仓库。

`update Apply` 的事务顺序：验证当前 metadata/markers → 下载 → 验签 → 校验 hash → 安全解包 → payload/ELF amd64 校验 → root owner/mode → 从固定系统路径执行 setcap/getcap → old→rollback → new→active → service install → restart → admin health + HTTP/HTTPS listener readiness。只有 readiness 成功才把 rollback 移入 backup。

任一步激活后失败：stop new → failed tree 移入 staging → old tree 恢复 → old service registration 恢复 → restart old → health/readiness old。返回值明确区分“更新失败但旧版已恢复”和“更新失败且回滚也失败”。

## 端口事务

`config set-ports` 只允许 root。流程为范围/互异校验 → 新端口预检（仅提示性）→ 原子写配置 → restart → admin health 和两个 proxy TCP readiness。失败时原子恢复旧配置、restart old 并复核旧端口。预检不被视为成功保证；最终 restart/readiness 才是 commit gate。

## 平台状态

Compatibility 值分为 `recognized`、`targeted`、`verified`。当前有完整 Adapter 的已知发行版仅标记 `targeted`；未知但可识别 ID 标记 `recognized`。只有 CI/release qualification 在具体版本完成安装、启动、停止、restart、自启、Web、80/443 capability、更新、自动 rollback 和卸载后才能标记 verified。

## 必须通过的门禁

常规 CI：

```sh
# Go 1.25.13+
go test ./...
go test -race ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@2025.1.1 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath ./cmd/sni-proxy
sh -n scripts/*.sh scripts/lib/*.sh scripts/tests/*.sh
shellcheck -s sh scripts/*.sh scripts/lib/*.sh scripts/tests/*.sh
```

发布资格仍需真实运行 Alpine 3.22 amd64/OpenRC/BusyBox/musl 和至少一个 systemd 发行版的完整安装到卸载 E2E。源码单元测试或仅容器内 build 不能把平台提升为 verified。
