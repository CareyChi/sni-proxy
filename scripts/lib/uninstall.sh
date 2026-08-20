#!/bin/sh

uninstall_sni_proxy() {
    [ "$(id -u)" -eq 0 ] || die "完全卸载需要 root 权限"
    require_safe_install_dir "$INSTALL_DIR"
    marker_file=$INSTALL_DIR/install-meta.json
    [ -f "$marker_file" ] && [ -f "$INSTALL_DIR/VERSION" ] && [ -f "$INSTALL_DIR/bin/sni-proxy" ] || \
        die "安装标记不完整，拒绝递归删除：$INSTALL_DIR"

    uninstall_config=$(metadata_value "$marker_file" config_dir)
    uninstall_data=$(metadata_value "$marker_file" data_dir)
    uninstall_log=$(metadata_value "$marker_file" log_dir)
    [ -n "$uninstall_config" ] || uninstall_config=$DEFAULT_CONFIG_DIR
    [ -n "$uninstall_data" ] || uninstall_data=$DEFAULT_DATA_DIR
    [ -n "$uninstall_log" ] || uninstall_log=$DEFAULT_LOG_DIR
    is_safe_absolute_dir "$uninstall_config" || die "Metadata 中的配置目录不安全"
    is_safe_absolute_dir "$uninstall_data" || die "Metadata 中的数据目录不安全"
    is_safe_absolute_dir "$uninstall_log" || die "Metadata 中的日志目录不安全"

    printf '将完全删除：\n  %s\n  %s\n  %s\n  %s\n' "$INSTALL_DIR" "$uninstall_config" "$uninstall_data" "$uninstall_log"
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

    rm -rf "$uninstall_config"
    rm -rf "$uninstall_data"
    rm -rf "$uninstall_log"
    rm -rf "$INSTALL_DIR"
    remove_system_user sni-proxy sni-proxy
    log_info 'SNI Proxy 已完全卸载。配置、数据、日志和程序目录均已删除，无法自动恢复。'
}
