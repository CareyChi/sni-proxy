#!/bin/sh

detect_init_system() {
    INIT_SYSTEM=unknown
    PID1_NAME=
    [ -r /proc/1/comm ] && PID1_NAME=$(sed -n '1p' /proc/1/comm)
    if [ -d /run/systemd/system ] && command_exists systemctl; then INIT_SYSTEM=systemd
    elif command_exists rc-service && command_exists rc-update; then INIT_SYSTEM=openrc
    elif command_exists sv && { command_exists runsvdir || [ -d /etc/sv ] || [ -d /var/service ]; }; then INIT_SYSTEM=runit
    elif { command_exists service || [ "$PID1_NAME" = init ]; } && [ -d /etc/init.d ]; then INIT_SYSTEM=sysvinit
    elif [ "$PID1_NAME" = systemd ] && command_exists systemctl; then INIT_SYSTEM=systemd
    fi
}

load_service_adapter() {
    [ -n "${INSTALL_DIR:-}" ] || INSTALL_DIR=$DEFAULT_INSTALL_DIR
    adapter_dir=$INSTALL_DIR/libexec
    [ -f "$adapter_dir/systemd.sh" ] || adapter_dir=$(dirname "$SCRIPT_LIB_DIR")/lib
    case $INIT_SYSTEM in
        systemd) . "$adapter_dir/systemd.sh" ;;
        openrc) . "$adapter_dir/openrc.sh" ;;
        sysvinit) . "$adapter_dir/sysv.sh" ;;
        runit) . "$adapter_dir/runit.sh" ;;
        *) return 1 ;;
    esac
}

service_start() { "${INIT_SYSTEM}_start"; }
service_stop() { "${INIT_SYSTEM}_stop"; }
service_restart() { "${INIT_SYSTEM}_restart"; }
service_is_running() { "${INIT_SYSTEM}_is_running"; }
service_enable() { "${INIT_SYSTEM}_enable"; }
service_disable() { "${INIT_SYSTEM}_disable"; }
service_is_enabled() { "${INIT_SYSTEM}_is_enabled"; }
service_install() { "${INIT_SYSTEM}_install"; }
service_uninstall() { "${INIT_SYSTEM}_uninstall"; }
