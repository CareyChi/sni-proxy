#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -P "$(dirname "$0")" 2>/dev/null && pwd -P) || exit 1
if [ -r "$SCRIPT_DIR/lib/common.sh" ]; then
    SCRIPT_LIB_DIR=$SCRIPT_DIR/lib
elif [ -r "$SCRIPT_DIR/libexec/common.sh" ]; then
    SCRIPT_LIB_DIR=$SCRIPT_DIR/libexec
elif [ -r "$SCRIPT_DIR/common.sh" ]; then
    SCRIPT_LIB_DIR=$SCRIPT_DIR
else
    printf '错误：缺少 common.sh\n' >&2
    exit 1
fi
. "$SCRIPT_LIB_DIR/common.sh"
. "$SCRIPT_LIB_DIR/distro.sh"
. "$SCRIPT_LIB_DIR/service.sh"

INSTALL_DIR=${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}
CONFIG_DIR=${CONFIG_DIR:-$DEFAULT_CONFIG_DIR}
DATA_DIR=${DATA_DIR:-$DEFAULT_DATA_DIR}
LOG_DIR=${LOG_DIR:-$DEFAULT_LOG_DIR}
if [ -n "${SNI_PROXY_PAYLOAD_DIR:-}" ]; then
    PAYLOAD_DIR=$SNI_PROXY_PAYLOAD_DIR
elif [ -f "$SCRIPT_DIR/bin/sni-proxy" ]; then
    PAYLOAD_DIR=$SCRIPT_DIR
else
    PAYLOAD_DIR=$(dirname "$SCRIPT_DIR")
fi
SERVICE_USER=sni-proxy
SERVICE_GROUP=sni-proxy
ALLOWED_DOMAINS=${SNI_PROXY_ALLOWED_DOMAINS:-}
RELEASE_PUBLIC_KEY=${SNI_PROXY_RELEASE_PUBLIC_KEY:-}
RELEASE_PUBLIC_KEY_FILE=${SNI_PROXY_RELEASE_PUBLIC_KEY_FILE:-}

require_root() {
    [ "$(id -u)" -eq 0 ] || die "请使用 root 运行安装程序"
}

require_linux() {
    detected_kernel=$(uname -s 2>/dev/null || printf unknown)
    [ "$detected_kernel" = Linux ] || die "仅支持 Linux，检测到：$detected_kernel"
    KERNEL_VERSION=$(uname -r 2>/dev/null || printf unknown)
}

print_environment() {
    printf '\n检测系统环境...\n\n'
    printf '系统：%s\n' "$DISTRO_NAME"
    printf '版本：%s\n' "$DISTRO_VERSION"
    printf '架构：%s\n' "$ARCHITECTURE"
    printf '内核：%s\n' "$KERNEL_VERSION"
    printf 'Init：%s\n' "$INIT_SYSTEM"
    printf '包管理器：%s\n' "$PACKAGE_MANAGER"
    printf '安装目录：%s\n\n' "$INSTALL_DIR"
    [ "$COMPATIBILITY_MODE" = generic ] && printf '兼容模式：Generic Linux（未声明为已验证发行版）\n\n'
}

download_file() {
    download_url=$1
    download_target=$2
    case $download_url in https://*) ;; *) die "只允许通过 HTTPS 下载安装包：$download_url" ;; esac
    if command_exists curl; then
        curl -fL "$download_url" -o "$download_target" || return 1
    elif command_exists wget; then
        wget -O "$download_target" "$download_url" || return 1
    else
        return 1
    fi
}

prepare_payload() {
    [ -f "$PAYLOAD_DIR/bin/sni-proxy" ] && return 0
    package_url=${SNI_PROXY_PACKAGE_URL:-}
    [ -n "$package_url" ] || return 0
    install_downloader_if_needed
    ensure_ca_bundle
    command_exists mktemp || die "缺少 mktemp，无法创建安全下载临时目录"
    download_root=$(mktemp -d "${TMPDIR:-/tmp}/sni-proxy-download.XXXXXX") || die "无法创建安全下载临时目录"
    DOWNLOAD_TMP=$download_root
    trap 'case ${DOWNLOAD_TMP:-} in */sni-proxy-download.*) rm -rf "$DOWNLOAD_TMP" ;; esac' 0
    package_archive=$download_root/sni-proxy-linux-amd64.tar.gz
    manifest_file=$download_root/sni-proxy-linux-amd64.manifest.json
    signature_file=$manifest_file.sig
    signature_binary=$download_root/manifest.sig.bin
    manifest_url=${SNI_PROXY_MANIFEST_URL:-${package_url%.tar.gz}.manifest.json}
    signature_url=${SNI_PROXY_SIGNATURE_URL:-${manifest_url}.sig}
    if [ ! -f "$RELEASE_PUBLIC_KEY_FILE" ] || [ -L "$RELEASE_PUBLIC_KEY_FILE" ]; then
        die "远程安装必须通过 SNI_PROXY_RELEASE_PUBLIC_KEY_FILE 提供非符号链接的可信 Ed25519 PEM 公钥"
    fi
    command_exists openssl || die "远程安装需要 openssl 验证 Ed25519 签名"
    public_key_uid=$(stat -c %u "$RELEASE_PUBLIC_KEY_FILE" 2>/dev/null) || die "无法检查发布公钥 owner"
    public_key_mode=$(stat -c %a "$RELEASE_PUBLIC_KEY_FILE" 2>/dev/null) || die "无法检查发布公钥权限"
    [ "$public_key_uid" = 0 ] || die "发布公钥必须由 root 拥有"
    case $public_key_mode in 400|440|444|600|640|644) ;; *) die "发布公钥权限不安全" ;; esac
    log_info "正在下载 SNI Proxy linux/amd64 安装包..."
    download_file "$package_url" "$package_archive" || die "下载安装包失败"
    download_file "$manifest_url" "$manifest_file" || die "下载签名 Manifest 失败"
    download_file "$signature_url" "$signature_file" || die "下载 Manifest 签名失败"
    openssl base64 -d -A -in "$signature_file" -out "$signature_binary" || die "Manifest 签名编码无效"
    openssl pkeyutl -verify -pubin -inkey "$RELEASE_PUBLIC_KEY_FILE" -rawin -in "$manifest_file" -sigfile "$signature_binary" >/dev/null 2>&1 || die "Manifest Ed25519 签名验证失败"
    manifest_architecture=$(sed -n 's/.*"architecture":"\([^"]*\)".*/\1/p' "$manifest_file")
    manifest_archive=$(sed -n 's/.*"archive":"\([^"]*\)".*/\1/p' "$manifest_file")
    expected_checksum=$(sed -n 's/.*"sha256":"\([^"]*\)".*/\1/p' "$manifest_file" | tr 'A-F' 'a-f')
    [ "$manifest_architecture" = amd64 ] || die "签名 Manifest 架构不匹配"
    [ "$manifest_archive" = sni-proxy-linux-amd64.tar.gz ] || die "签名 Manifest 文件名不匹配"
    case $expected_checksum in
        *[!0-9a-f]*|'') die "SHA-256 校验文件格式无效" ;;
    esac
    [ "${#expected_checksum}" -eq 64 ] || die "SHA-256 校验值长度无效"
    if command_exists sha256sum; then
        actual_checksum=$(sha256sum "$package_archive" | awk '{ print $1 }')
    elif command_exists openssl; then
        actual_checksum=$(openssl dgst -sha256 "$package_archive" | awk '{ print $NF }')
    else
        die "缺少 sha256sum/openssl，无法安全验证安装包"
    fi
    actual_checksum=$(printf '%s' "$actual_checksum" | tr 'A-F' 'a-f')
    [ "$actual_checksum" = "$expected_checksum" ] || die "安装包 SHA-256 校验失败"
    if [ -z "$RELEASE_PUBLIC_KEY" ]; then
        RELEASE_PUBLIC_KEY=$(openssl pkey -pubin -in "$RELEASE_PUBLIC_KEY_FILE" -outform DER 2>/dev/null | tail -c 32 | openssl base64 -A | tr -d '=\n') || die "无法导出更新信任公钥"
        [ -n "$RELEASE_PUBLIC_KEY" ] || die "无法导出更新信任公钥"
    fi
    command_exists tar || die "缺少 tar，无法解压安装包"
    if tar -tzf "$package_archive" | awk '
        /(^|\/)\.\.($|\/)/ || /^\// || /\\/ { unsafe=1 }
        END { exit unsafe ? 0 : 1 }
    '; then
        die "安装包包含不安全路径"
    fi
    mkdir "$download_root/payload" || die "无法创建解压目录"
    tar -xzf "$package_archive" -C "$download_root/payload" || die "解压安装包失败"
    PAYLOAD_DIR=$download_root/payload
}

install_setcap_tool() {
    command_exists setcap && command_exists getcap && return 0
    case $PACKAGE_MANAGER in
        apt) capability_package=libcap2-bin ;;
        dnf|yum|apk|zypper|pacman) capability_package=libcap ;;
        emerge) capability_package=sys-libs/libcap ;;
        xbps) capability_package=libcap-progs ;;
        *) return 1 ;;
    esac
    log_info "正在安装低端口能力工具：$capability_package"
    pkg_update || return 1
    pkg_install "$capability_package"
}

configure_low_ports() {
    binary_path=$INSTALL_DIR/bin/sni-proxy
    install_setcap_tool || die "缺少 setcap/getcap，无法以低权限用户安全监听 80/443"
    setcap cap_net_bind_service=+ep "$binary_path" || die "设置 CAP_NET_BIND_SERVICE 失败；文件系统可能不支持 File Capability"
    capability_value=$(getcap "$binary_path" 2>/dev/null || true)
    case $capability_value in
        *cap_net_bind_service*) ;;
        *) die "CAP_NET_BIND_SERVICE 验证失败，拒绝以未声明方式长期使用 root 运行" ;;
    esac
}

render_service_templates() {
    escaped_install=$(sed_replacement "$INSTALL_DIR")
    escaped_config=$(sed_replacement "$CONFIG_DIR")
    escaped_data=$(sed_replacement "$DATA_DIR")
    escaped_log=$(sed_replacement "$LOG_DIR")
    escaped_user=$(sed_replacement "$SERVICE_USER")
    escaped_group=$(sed_replacement "$SERVICE_GROUP")
    for template_file in \
        "$INSTALL_DIR/service/systemd/sni-proxy.service" \
        "$INSTALL_DIR/service/openrc/sni-proxy" \
        "$INSTALL_DIR/service/sysvinit/sni-proxy" \
        "$INSTALL_DIR/service/runit/sni-proxy/run"; do
        rendered_file=$template_file.rendered
        sed \
            -e "s|@INSTALL_DIR@|$escaped_install|g" \
            -e "s|@CONFIG_DIR@|$escaped_config|g" \
            -e "s|@DATA_DIR@|$escaped_data|g" \
            -e "s|@LOG_DIR@|$escaped_log|g" \
            -e "s|@SERVICE_USER@|$escaped_user|g" \
            -e "s|@SERVICE_GROUP@|$escaped_group|g" \
            "$template_file" > "$rendered_file" || die "渲染服务模板失败：$template_file"
        mv "$rendered_file" "$template_file" || die "替换服务模板失败：$template_file"
    done
    chmod 644 "$INSTALL_DIR/service/systemd/sni-proxy.service"
    chmod 755 "$INSTALL_DIR/service/openrc/sni-proxy" "$INSTALL_DIR/service/sysvinit/sni-proxy" "$INSTALL_DIR/service/runit/sni-proxy/run"
}

install_payload() {
    [ -f "$PAYLOAD_DIR/bin/sni-proxy" ] || die "未找到 linux/amd64 核心程序：$PAYLOAD_DIR/bin/sni-proxy（当前阶段不自动编译）"
    mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/libexec" "$INSTALL_DIR/service/systemd" "$INSTALL_DIR/service/openrc" \
        "$INSTALL_DIR/service/sysvinit" "$INSTALL_DIR/service/runit/sni-proxy" "$INSTALL_DIR/templates" \
        "$CONFIG_DIR" "$DATA_DIR/backups" "$LOG_DIR" || die "无法创建安装目录"
    write_managed_marker "$INSTALL_DIR" install "$INSTALLATION_ID" || die "无法写入程序目录管理标记"
    write_managed_marker "$CONFIG_DIR" config "$INSTALLATION_ID" || die "无法写入配置目录管理标记"
    write_managed_marker "$DATA_DIR" data "$INSTALLATION_ID" || die "无法写入数据目录管理标记"
    write_managed_marker "$LOG_DIR" log "$INSTALLATION_ID" || die "无法写入日志目录管理标记"

    payload_lib=$SCRIPT_LIB_DIR
    [ -f "$PAYLOAD_DIR/libexec/common.sh" ] && payload_lib=$PAYLOAD_DIR/libexec
    control_source=$SCRIPT_DIR/sni-proxyctl
    [ -f "$control_source" ] || control_source=$PAYLOAD_DIR/bin/sni-proxyctl
    installer_source=$SCRIPT_DIR/install.sh
    [ -f "$PAYLOAD_DIR/install.sh" ] && installer_source=$PAYLOAD_DIR/install.sh
    copy_file "$PAYLOAD_DIR/bin/sni-proxy" "$INSTALL_DIR/bin/sni-proxy" 755
    copy_file "$control_source" "$INSTALL_DIR/bin/sni-proxyctl" 755
    copy_file "$installer_source" "$INSTALL_DIR/libexec/install.sh" 755
    for library in common.sh distro.sh service.sh systemd.sh openrc.sh sysv.sh runit.sh network.sh update.sh uninstall.sh; do
        copy_file "$payload_lib/$library" "$INSTALL_DIR/libexec/$library" 755
    done
    copy_file "$PAYLOAD_DIR/service/systemd/sni-proxy.service" "$INSTALL_DIR/service/systemd/sni-proxy.service" 644
    copy_file "$PAYLOAD_DIR/service/openrc/sni-proxy" "$INSTALL_DIR/service/openrc/sni-proxy" 755
    copy_file "$PAYLOAD_DIR/service/sysvinit/sni-proxy" "$INSTALL_DIR/service/sysvinit/sni-proxy" 755
    copy_file "$PAYLOAD_DIR/service/runit/sni-proxy/run" "$INSTALL_DIR/service/runit/sni-proxy/run" 755
    copy_file "$PAYLOAD_DIR/VERSION" "$INSTALL_DIR/VERSION" 644
    render_service_templates

    mkdir -p /usr/local/bin || die "无法创建 /usr/local/bin"
    create_entry_symlink "$INSTALL_DIR/bin/sni-proxy" /usr/local/bin/sni-proxy
    create_entry_symlink "$INSTALL_DIR/bin/sni-proxyctl" /usr/local/bin/sni-proxyctl
}

write_install_metadata() {
    metadata_file=$INSTALL_DIR/install-meta.json
    metadata_tmp=$metadata_file.tmp
    umask 022
    {
        printf '{\n'
        printf '  "installation_id": "%s",\n' "$(json_escape "$INSTALLATION_ID")"
        printf '  "install_dir": "%s",\n' "$(json_escape "$INSTALL_DIR")"
        printf '  "config_dir": "%s",\n' "$(json_escape "$CONFIG_DIR")"
        printf '  "data_dir": "%s",\n' "$(json_escape "$DATA_DIR")"
        printf '  "log_dir": "%s",\n' "$(json_escape "$LOG_DIR")"
        printf '  "distribution_id": "%s",\n' "$(json_escape "$DISTRO_ID")"
        printf '  "distribution_version": "%s",\n' "$(json_escape "$DISTRO_VERSION")"
        printf '  "init_system": "%s",\n' "$(json_escape "$INIT_SYSTEM")"
        printf '  "architecture": "%s"\n' "$(json_escape "$ARCHITECTURE")"
        printf '}\n'
    } > "$metadata_tmp" || die "无法写入安装 Metadata"
    chmod 644 "$metadata_tmp"
    mv "$metadata_tmp" "$metadata_file" || die "无法激活安装 Metadata"
}

prepare_installation_identity() {
    existing_metadata=$INSTALL_DIR/install-meta.json
    if [ -f "$existing_metadata" ]; then
        INSTALLATION_ID=$(metadata_value "$existing_metadata" installation_id)
        [ -n "$INSTALLATION_ID" ] || die "现有安装缺少 installation_id，拒绝覆盖；请先人工迁移或清理"
        validate_managed_marker "$INSTALL_DIR" install "$INSTALLATION_ID" || die "现有程序目录管理标记无效"
        validate_managed_marker "$CONFIG_DIR" config "$INSTALLATION_ID" || die "现有配置目录管理标记无效"
        validate_managed_marker "$DATA_DIR" data "$INSTALLATION_ID" || die "现有数据目录管理标记无效"
        validate_managed_marker "$LOG_DIR" log "$INSTALLATION_ID" || die "现有日志目录管理标记无效"
        return 0
    fi
    for identity_dir in "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR" "$LOG_DIR"; do
        if [ -d "$identity_dir" ] && [ -n "$(ls -A "$identity_dir" 2>/dev/null)" ]; then
            die "目录已包含非托管内容，拒绝接管：$identity_dir"
        fi
        [ ! -e "$identity_dir" ] || [ -d "$identity_dir" ] || die "受管理路径不是目录：$identity_dir"
    done
    INSTALLATION_ID=$(new_installation_id) || die "无法生成安全 installation_id"
}

require_allowed_domains() {
    if [ -z "$ALLOWED_DOMAINS" ]; then
        printf '请输入允许代理的域名（逗号分隔，不能为空）：'
        IFS= read -r ALLOWED_DOMAINS
    fi
    [ -n "$ALLOWED_DOMAINS" ] || die "安全默认值为拒绝全部目标；安装时必须显式提供域名 allowlist"
}

write_update_trust_policy() {
    trust_file=$CONFIG_DIR/update-trust.json
    trust_tmp=$trust_file.tmp
    umask 022
    {
        printf '{\n'
        printf '  "repository": "CareyChi/sni-proxy",\n'
        printf '  "ed25519_public_key": "%s"\n' "$(json_escape "$RELEASE_PUBLIC_KEY")"
        printf '}\n'
    } > "$trust_tmp" || die "无法写入更新信任策略"
    chown root:root "$trust_tmp" || die "无法设置更新信任策略 owner"
    chmod 644 "$trust_tmp" || die "无法设置更新信任策略权限"
    mv "$trust_tmp" "$trust_file" || die "无法激活更新信任策略"
    [ -n "$RELEASE_PUBLIC_KEY" ] || log_warn "未提供 SNI_PROXY_RELEASE_PUBLIC_KEY；签名密钥配置前将拒绝执行更新"
}

initialize_application() {
    chown -R root:root "$INSTALL_DIR" "$CONFIG_DIR"
    chown -R "$SERVICE_USER:$SERVICE_GROUP" "$DATA_DIR" "$LOG_DIR"
    chmod 755 "$CONFIG_DIR"
    chmod 750 "$DATA_DIR" "$LOG_DIR" "$DATA_DIR/backups"
    "$INSTALL_DIR/bin/sni-proxy" config set-ports --initialize --http 80 --https 443 --web 6866 --allow-domains "$ALLOWED_DOMAINS" || die "初始化端口和域名 allowlist 失败"
    credential_output=$("$INSTALL_DIR/bin/sni-proxy" admin init --username admin --generate) || die "初始化管理员凭据失败"
    chown -R root:root "$CONFIG_DIR"
    chmod 755 "$CONFIG_DIR"
    chmod 644 "$CONFIG_DIR/config.json"
    chown -R "$SERVICE_USER:$SERVICE_GROUP" "$DATA_DIR" "$LOG_DIR"
    write_update_trust_policy
    for root_marker in "$INSTALL_DIR/.sni-proxy-managed" "$CONFIG_DIR/.sni-proxy-managed" "$DATA_DIR/.sni-proxy-managed" "$LOG_DIR/.sni-proxy-managed"; do
        chown root:root "$root_marker" || die "无法恢复管理标记 owner：$root_marker"
        chmod 644 "$root_marker" || die "无法恢复管理标记权限：$root_marker"
    done
    configure_low_ports
    service_install || die "注册 $INIT_SYSTEM 服务失败"
    service_enable || die "开启开机自启失败"
    service_start || die "启动 SNI Proxy 失败"
    health_attempt=0
    while [ "$health_attempt" -lt 30 ]; do
        if "$INSTALL_DIR/bin/sni-proxy" health --timeout 2s >/dev/null 2>&1; then break; fi
        health_attempt=$((health_attempt + 1))
        sleep 1
    done
    [ "$health_attempt" -lt 30 ] || die "服务已启动但健康检查未通过"
}

print_success() {
    printf '\n========================================\n'
    printf ' SNI Proxy 安装成功\n'
    printf '========================================\n'
    printf ' 系统：%s\n' "$DISTRO_NAME"
    printf ' Init：%s\n' "$INIT_SYSTEM"
    printf ' 安装目录：%s\n' "$INSTALL_DIR"
    printf ' 核心程序：运行中\n'
    printf ' 开机自启：已开启\n\n'
    "$INSTALL_DIR/bin/sni-proxy" info | sed -n '/^后台地址：/p'
    printf '%s\n' "$credential_output"
    printf '\n管理命令：sni-proxyctl\n'
    printf '========================================\n'
}

require_root
require_linux
validate_managed_dirs || die "安装、配置、数据和日志目录必须规范、位于允许空间且互不重叠"
detect_architecture
detect_distribution
detect_package_manager
detect_container
detect_init_system
[ "$INIT_SYSTEM" != unknown ] || die "当前环境未检测到受支持的服务管理器。可使用 sni-proxy serve 手动运行，但无法安装开机自启服务。"
print_environment
require_allowed_domains
prepare_installation_identity
prepare_payload
create_system_user "$SERVICE_USER" "$SERVICE_GROUP"
install_payload
load_service_adapter || die "无法加载 $INIT_SYSTEM Service Adapter"
write_install_metadata
initialize_application
print_success
[ -n "${DOWNLOAD_TMP:-}" ] && rm -rf "$DOWNLOAD_TMP"
