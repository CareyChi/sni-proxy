#!/bin/sh

network_check_port() {
    "$INSTALL_DIR/bin/sni-proxy" network check-port --host "${1:-0.0.0.0}" --port "$2"
}

network_show_info() {
    "$INSTALL_DIR/bin/sni-proxy" info
}
