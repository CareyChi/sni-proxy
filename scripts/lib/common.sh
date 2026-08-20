#!/bin/sh

DEFAULT_INSTALL_DIR=/opt/sni-proxy
DEFAULT_CONFIG_DIR=/etc/sni-proxy
DEFAULT_DATA_DIR=/var/lib/sni-proxy
DEFAULT_LOG_DIR=/var/log/sni-proxy

log_info() { printf '%s\n' "$*"; }
log_warn() { printf '警告：%s\n' "$*" >&2; }
die() { printf '错误：%s\n' "$*" >&2; exit 1; }
command_exists() { command -v "$1" >/dev/null 2>&1; }

resolve_symlink() {
    resolve_target=$1
    case $resolve_target in
        /*) ;;
        *) resolve_target=$(pwd -P)/$resolve_target ;;
    esac
    resolve_count=0
    while [ -L "$resolve_target" ]; do
        resolve_count=$((resolve_count + 1))
        [ "$resolve_count" -le 40 ] || return 1
        command_exists readlink || return 1
        resolve_link=$(readlink "$resolve_target") || return 1
        case $resolve_link in
            /*) resolve_target=$resolve_link ;;
            *) resolve_target=$(dirname "$resolve_target")/$resolve_link ;;
        esac
    done
    resolve_dir=$(CDPATH= cd -P "$(dirname "$resolve_target")" 2>/dev/null && pwd -P) || return 1
    printf '%s/%s\n' "$resolve_dir" "$(basename "$resolve_target")"
}

is_safe_absolute_dir() {
    safe_dir=$1
    case $safe_dir in
        ''|/|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/var) return 1 ;;
        /*) return 0 ;;
        *) return 1 ;;
    esac
}

require_safe_install_dir() {
    is_safe_absolute_dir "$1" || die "拒绝使用危险安装目录：${1:-<空>}"
}

find_nologin_shell() {
    for shell_path in /usr/sbin/nologin /sbin/nologin /bin/false; do
        if [ -x "$shell_path" ]; then
            printf '%s\n' "$shell_path"
            return 0
        fi
    done
    return 1
}

user_exists() {
    if command_exists getent; then
        getent passwd "$1" >/dev/null 2>&1
    else
        awk -F: -v wanted="$1" '$1 == wanted { found=1 } END { exit found ? 0 : 1 }' /etc/passwd
    fi
}

group_exists() {
    if command_exists getent; then
        getent group "$1" >/dev/null 2>&1
    else
        awk -F: -v wanted="$1" '$1 == wanted { found=1 } END { exit found ? 0 : 1 }' /etc/group
    fi
}

create_system_user() {
    account_user=$1
    account_group=$2
    account_shell=$(find_nologin_shell) || die "未找到 nologin 或 /bin/false"
    if ! group_exists "$account_group"; then
        if command_exists groupadd; then
            groupadd --system "$account_group" 2>/dev/null || groupadd -r "$account_group"
        elif command_exists addgroup; then
            addgroup -S "$account_group"
        else
            die "系统缺少 groupadd/addgroup，无法创建服务组"
        fi
    fi
    if ! user_exists "$account_user"; then
        if command_exists useradd; then
            useradd --system --gid "$account_group" --no-create-home --shell "$account_shell" "$account_user" 2>/dev/null || \
                useradd -r -g "$account_group" -M -s "$account_shell" "$account_user"
        elif command_exists adduser; then
            adduser -S -D -H -G "$account_group" -s "$account_shell" "$account_user"
        else
            die "系统缺少 useradd/adduser，无法创建服务用户"
        fi
    fi
}

remove_system_user() {
    account_user=$1
    account_group=$2
    if user_exists "$account_user"; then
        if command_exists userdel; then
            userdel "$account_user" 2>/dev/null || true
        elif command_exists deluser; then
            deluser "$account_user" 2>/dev/null || true
        fi
    fi
    if group_exists "$account_group"; then
        if command_exists groupdel; then
            groupdel "$account_group" 2>/dev/null || true
        elif command_exists delgroup; then
            delgroup "$account_group" 2>/dev/null || true
        fi
    fi
}

copy_file() {
    copy_source=$1
    copy_target=$2
    copy_mode=$3
    [ -f "$copy_source" ] || die "缺少安装资源：$copy_source"
    if [ "$copy_source" = "$copy_target" ]; then
        chmod "$copy_mode" "$copy_target" || die "无法设置权限：$copy_target"
        return 0
    fi
    mkdir -p "$(dirname "$copy_target")" || die "无法创建目录：$(dirname "$copy_target")"
    cp "$copy_source" "$copy_target" || die "无法复制：$copy_source"
    chmod "$copy_mode" "$copy_target" || die "无法设置权限：$copy_target"
}

create_entry_symlink() {
    entry_target=$1
    entry_link=$2
    if [ -e "$entry_link" ] && [ ! -L "$entry_link" ]; then
        die "$entry_link 已存在且不是符号链接，拒绝覆盖"
    fi
    rm -f "$entry_link"
    ln -s "$entry_target" "$entry_link" || die "无法创建入口链接：$entry_link"
}

json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/[[:cntrl:]]//g'
}

sed_replacement() {
    printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}

metadata_value() {
    metadata_file=$1
    metadata_key=$2
    [ -f "$metadata_file" ] || return 1
    sed -n "s/^[[:space:]]*\"$metadata_key\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$metadata_file" | sed -n '1p'
}
