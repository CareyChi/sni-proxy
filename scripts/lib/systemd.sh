#!/bin/sh

systemd_start() { systemctl start sni-proxy; }
systemd_stop() { systemctl stop sni-proxy; }
systemd_restart() { systemctl restart sni-proxy; }
systemd_is_running() { systemctl is-active --quiet sni-proxy; }
systemd_enable() { systemctl enable sni-proxy; }
systemd_disable() { systemctl disable sni-proxy; }
systemd_is_enabled() { systemctl is-enabled --quiet sni-proxy; }
systemd_install() {
    systemd_source=$INSTALL_DIR/service/systemd/sni-proxy.service
    systemd_target=/etc/systemd/system/sni-proxy.service
    rm -f "$systemd_target"
    ln -s "$systemd_source" "$systemd_target" || return 1
    systemctl daemon-reload
}
systemd_uninstall() {
    rm -f /etc/systemd/system/sni-proxy.service
    systemctl daemon-reload
}
