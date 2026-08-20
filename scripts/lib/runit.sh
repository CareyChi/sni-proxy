#!/bin/sh

detect_runit_service_dir() {
    if [ -n "${RUNIT_SERVICE_DIR:-}" ]; then return 0; fi
    for runit_candidate in /var/service /run/service /etc/service; do
        if [ -d "$runit_candidate" ]; then RUNIT_SERVICE_DIR=$runit_candidate; return 0; fi
    done
    RUNIT_SERVICE_DIR=/var/service
}
runit_service_path() { detect_runit_service_dir; printf '%s/sni-proxy\n' "$RUNIT_SERVICE_DIR"; }
runit_start() { sv up "$(runit_service_path)"; }
runit_stop() { sv down "$(runit_service_path)"; }
runit_restart() { sv restart "$(runit_service_path)"; }
runit_is_running() { sv status "$(runit_service_path)" >/dev/null 2>&1; }
runit_enable() {
    detect_runit_service_dir
    mkdir -p "$RUNIT_SERVICE_DIR" || return 1
    [ -L "$RUNIT_SERVICE_DIR/sni-proxy" ] || ln -s "$INSTALL_DIR/service/runit/sni-proxy" "$RUNIT_SERVICE_DIR/sni-proxy"
}
runit_disable() { rm -f "$(runit_service_path)"; }
runit_is_enabled() { [ -L "$(runit_service_path)" ] || [ -d "$(runit_service_path)" ]; }
runit_install() { chmod 755 "$INSTALL_DIR/service/runit/sni-proxy/run"; }
runit_uninstall() { rm -f "$(runit_service_path)"; }
