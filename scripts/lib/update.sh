#!/bin/sh

update_check() { "$INSTALL_DIR/bin/sni-proxy" update check; }
update_apply() { "$INSTALL_DIR/bin/sni-proxy" update apply --version "$1"; }
