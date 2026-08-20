#!/bin/sh

openrc_start() { rc-service sni-proxy start; }
openrc_stop() { rc-service sni-proxy stop; }
openrc_restart() { rc-service sni-proxy restart; }
openrc_is_running() { rc-service sni-proxy status >/dev/null 2>&1; }
openrc_enable() { rc-update add sni-proxy default; }
openrc_disable() { rc-update del sni-proxy default; }
openrc_is_enabled() { rc-update show default 2>/dev/null | awk '$1 == "sni-proxy" { found=1 } END { exit found ? 0 : 1 }'; }
openrc_install() {
    openrc_source=$INSTALL_DIR/service/openrc/sni-proxy
    openrc_target=/etc/init.d/sni-proxy
    chmod 755 "$openrc_source" || return 1
    rm -f "$openrc_target"
    ln -s "$openrc_source" "$openrc_target"
}
openrc_uninstall() { rm -f /etc/init.d/sni-proxy; }
