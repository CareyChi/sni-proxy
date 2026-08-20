#!/bin/sh

sysv_start() { service sni-proxy start; }
sysv_stop() { service sni-proxy stop; }
sysv_restart() { service sni-proxy restart; }
sysv_is_running() { service sni-proxy status >/dev/null 2>&1; }
sysv_enable() {
    if command_exists update-rc.d; then update-rc.d sni-proxy defaults
    elif command_exists chkconfig; then chkconfig sni-proxy on
    else
        for sysv_level in 2 3 4 5; do
            mkdir -p "/etc/rc${sysv_level}.d" || return 1
            ln -s ../init.d/sni-proxy "/etc/rc${sysv_level}.d/S90sni-proxy" 2>/dev/null || [ -L "/etc/rc${sysv_level}.d/S90sni-proxy" ] || return 1
        done
    fi
}
sysv_disable() {
    if command_exists update-rc.d; then update-rc.d -f sni-proxy remove
    elif command_exists chkconfig; then chkconfig sni-proxy off
    else
        for sysv_level in 0 1 2 3 4 5 6; do rm -f "/etc/rc${sysv_level}.d/S90sni-proxy"; done
    fi
}
sysv_is_enabled() {
    for sysv_entry in /etc/rc[2345].d/S??sni-proxy; do
        [ -e "$sysv_entry" ] || [ -L "$sysv_entry" ] || continue
        return 0
    done
    return 1
}
sysv_install() {
    sysv_source=$INSTALL_DIR/service/sysvinit/sni-proxy
    sysv_target=/etc/init.d/sni-proxy
    chmod 755 "$sysv_source" || return 1
    rm -f "$sysv_target"
    ln -s "$sysv_source" "$sysv_target"
}
sysv_uninstall() { rm -f /etc/init.d/sni-proxy; }
