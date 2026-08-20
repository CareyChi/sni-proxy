#!/bin/sh

uninstall_sni_proxy() {
    [ "$(id -u)" -eq 0 ] || die "完全卸载需要 root 权限"
    INSTALL_DIR=$(canonical_managed_dir install "$INSTALL_DIR") || die "程序目录不安全"
    marker_file=$INSTALL_DIR/install-meta.json
    if [ ! -f "$marker_file" ] || [ ! -f "$INSTALL_DIR/VERSION" ] || [ ! -f "$INSTALL_DIR/bin/sni-proxy" ]; then
        die "安装标记不完整，拒绝递归删除：$INSTALL_DIR"
    fi

    uninstall_config=$(metadata_value "$marker_file" config_dir)
    uninstall_data=$(metadata_value "$marker_file" data_dir)
    uninstall_log=$(metadata_value "$marker_file" log_dir)
    uninstall_install=$(metadata_value "$marker_file" install_dir)
    uninstall_id=$(metadata_value "$marker_file" installation_id)
    [ -n "$uninstall_config" ] || uninstall_config=$DEFAULT_CONFIG_DIR
    [ -n "$uninstall_data" ] || uninstall_data=$DEFAULT_DATA_DIR
    [ -n "$uninstall_log" ] || uninstall_log=$DEFAULT_LOG_DIR
    [ -n "$uninstall_id" ] || die "Metadata 缺少 installation_id"
    [ "$uninstall_install" = "$INSTALL_DIR" ] || die "Metadata 中的程序目录与实际目录不一致"
    CONFIG_DIR=$uninstall_config
    DATA_DIR=$uninstall_data
    LOG_DIR=$uninstall_log
    validate_managed_dirs || die "Metadata 中的受管理目录不规范、越界或互相重叠"
    validate_managed_marker "$INSTALL_DIR" install "$uninstall_id" || die "程序目录管理标记无效"
    validate_managed_marker "$CONFIG_DIR" config "$uninstall_id" || die "配置目录管理标记无效"
    validate_managed_marker "$DATA_DIR" data "$uninstall_id" || die "数据目录管理标记无效"
    validate_managed_marker "$LOG_DIR" log "$uninstall_id" || die "日志目录管理标记无效"

    printf '将完全删除：\n  %s\n  %s\n  %s\n  %s\n' "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR" "$LOG_DIR"
    printf '请输入 DELETE 确认完全卸载：'
    IFS= read -r uninstall_confirm
    [ "$uninstall_confirm" = DELETE ] || { log_info '已取消卸载。'; return 0; }

    service_stop >/dev/null 2>&1 || true
    service_disable >/dev/null 2>&1 || true
    service_uninstall >/dev/null 2>&1 || true

    for entry_link in /usr/local/bin/sni-proxy /usr/local/bin/sni-proxyctl; do
        if [ -L "$entry_link" ]; then
            entry_target=$(readlink "$entry_link" 2>/dev/null || true)
            case $entry_target in "$INSTALL_DIR"/*) rm -f "$entry_link" ;; esac
        fi
    done

    rm -rf "$CONFIG_DIR"
    rm -rf "$DATA_DIR"
    rm -rf "$LOG_DIR"
    rm -rf "$INSTALL_DIR"
    remove_system_user sni-proxy sni-proxy
    log_info 'SNI Proxy 已完全卸载。配置、数据、日志和程序目录均已删除，无法自动恢复。'
}
