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
    case "/${safe_dir#/}/" in
        */./*|*/../*|*'//'*) return 1 ;;
    esac
    [ "$safe_dir" = "${safe_dir%/}" ] || return 1
    case $safe_dir in
        ''|/|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/var) return 1 ;;
        /*) canonicalize_dir "$safe_dir" >/dev/null ;;
        *) return 1 ;;
    esac
}

require_safe_install_dir() {
    canonical_managed_dir install "$1" >/dev/null || die "拒绝使用危险安装目录：${1:-<空>}"
}

canonicalize_dir() {
    canonical_input=$1
    case $canonical_input in /*) ;; *) return 1 ;; esac
    case $canonical_input in *[!A-Za-z0-9_./-]*) return 1 ;; esac
    case "/${canonical_input#/}/" in */./*|*/../*|*'//'*) return 1 ;; esac
    [ "$canonical_input" = "${canonical_input%/}" ] || return 1
    canonical_parent=$canonical_input
    canonical_suffix=
    while [ ! -e "$canonical_parent" ] && [ ! -L "$canonical_parent" ]; do
        canonical_base=$(basename "$canonical_parent") || return 1
        canonical_suffix=/$canonical_base$canonical_suffix
        canonical_next=$(dirname "$canonical_parent") || return 1
        [ "$canonical_next" != "$canonical_parent" ] || return 1
        canonical_parent=$canonical_next
    done
    [ -d "$canonical_parent" ] || return 1
    canonical_resolved=$(CDPATH= cd -P "$canonical_parent" 2>/dev/null && pwd -P) || return 1
    [ "$canonical_resolved" != / ] || canonical_resolved=
    printf '%s%s\n' "$canonical_resolved" "$canonical_suffix"
}

canonical_managed_dir() {
    managed_role=$1
    managed_input=$2
    managed_canonical=$(canonicalize_dir "$managed_input") || return 1
    case $managed_role:$managed_canonical in
        install:/opt/*|install:/srv/*|install:/usr/local/lib/*) ;;
        config:/etc/*) ;;
        data:/var/lib/*) ;;
        log:/var/log/*) ;;
        *) return 1 ;;
    esac
    printf '%s\n' "$managed_canonical"
}

path_contains() {
    contains_parent=$1
    contains_child=$2
    [ "$contains_parent" = "$contains_child" ] && return 0
    case $contains_child in "$contains_parent"/*) return 0 ;; *) return 1 ;; esac
}

paths_overlap() {
    path_contains "$1" "$2" || path_contains "$2" "$1"
}

validate_managed_dirs() {
    INSTALL_DIR=$(canonical_managed_dir install "$INSTALL_DIR") || return 1
    CONFIG_DIR=$(canonical_managed_dir config "$CONFIG_DIR") || return 1
    DATA_DIR=$(canonical_managed_dir data "$DATA_DIR") || return 1
    LOG_DIR=$(canonical_managed_dir log "$LOG_DIR") || return 1
    paths_overlap "$INSTALL_DIR" "$CONFIG_DIR" && return 1
    paths_overlap "$INSTALL_DIR" "$DATA_DIR" && return 1
    paths_overlap "$INSTALL_DIR" "$LOG_DIR" && return 1
    paths_overlap "$CONFIG_DIR" "$DATA_DIR" && return 1
    paths_overlap "$CONFIG_DIR" "$LOG_DIR" && return 1
    paths_overlap "$DATA_DIR" "$LOG_DIR" && return 1
    return 0
}

new_installation_id() {
    command_exists od || return 1
    installation_random=$(od -An -N16 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n') || return 1
    case $installation_random in ''|*[!0-9a-f]*) return 1 ;; esac
    [ "${#installation_random}" -eq 32 ] || return 1
    printf '%s\n' "$installation_random"
}

write_managed_marker() {
    marker_dir=$1
    marker_role=$2
    marker_id=$3
    marker_file=$marker_dir/.sni-proxy-managed
    marker_tmp=$marker_file.tmp
    umask 022
    {
        printf '{"installation_id":"%s","role":"%s"}\n' "$marker_id" "$marker_role"
    } > "$marker_tmp" || return 1
    chown root:root "$marker_tmp" || return 1
    chmod 644 "$marker_tmp" || return 1
    mv "$marker_tmp" "$marker_file"
}

validate_managed_marker() {
    marker_dir=$1
    marker_role=$2
    marker_id=$3
    marker_file=$marker_dir/.sni-proxy-managed
    [ -f "$marker_file" ] && [ ! -L "$marker_file" ] || return 1
    [ "$(metadata_value "$marker_file" installation_id)" = "$marker_id" ] || return 1
    [ "$(metadata_value "$marker_file" role)" = "$marker_role" ] || return 1
    marker_uid=$(stat -c %u "$marker_file" 2>/dev/null) || return 1
    marker_mode=$(stat -c %a "$marker_file" 2>/dev/null) || return 1
    [ "$marker_uid" = 0 ] || return 1
    case $marker_mode in 600|644) return 0 ;; *) return 1 ;; esac
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
