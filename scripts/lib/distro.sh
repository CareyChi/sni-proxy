#!/bin/sh

os_release_value() {
    release_file=$1
    release_key=$2
    [ -r "$release_file" ] || return 1
    awk -F= -v wanted="$release_key" '
        $1 == wanted {
            value=substr($0, index($0, "=")+1)
            if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) value=substr(value, 2, length(value)-2)
            gsub(/\\"/, "\"", value)
            gsub(/\\\\/, "\\", value)
            print value
            exit
        }
    ' "$release_file"
}

detect_distribution() {
    OS_RELEASE_FILE=
    if [ -r /etc/os-release ]; then
        OS_RELEASE_FILE=/etc/os-release
    elif [ -r /usr/lib/os-release ]; then
        OS_RELEASE_FILE=/usr/lib/os-release
    fi
    if [ -n "$OS_RELEASE_FILE" ]; then
        DISTRO_ID=$(os_release_value "$OS_RELEASE_FILE" ID)
        DISTRO_ID_LIKE=$(os_release_value "$OS_RELEASE_FILE" ID_LIKE)
        DISTRO_NAME=$(os_release_value "$OS_RELEASE_FILE" PRETTY_NAME)
        [ -n "$DISTRO_NAME" ] || DISTRO_NAME=$(os_release_value "$OS_RELEASE_FILE" NAME)
        DISTRO_VERSION=$(os_release_value "$OS_RELEASE_FILE" VERSION_ID)
        [ -n "$DISTRO_VERSION" ] || DISTRO_VERSION=$(os_release_value "$OS_RELEASE_FILE" VERSION)
    elif command_exists lsb_release; then
        DISTRO_NAME=$(lsb_release -si 2>/dev/null || printf 'Unknown Linux')
        DISTRO_ID=$(printf '%s' "$DISTRO_NAME" | tr 'A-Z ' 'a-z-')
        DISTRO_VERSION=$(lsb_release -sr 2>/dev/null || printf unknown)
    elif [ -r /etc/alpine-release ]; then
        DISTRO_ID=alpine; DISTRO_NAME='Alpine Linux'; DISTRO_VERSION=$(sed -n '1p' /etc/alpine-release)
    elif [ -r /etc/debian_version ]; then
        DISTRO_ID=debian; DISTRO_NAME=Debian; DISTRO_VERSION=$(sed -n '1p' /etc/debian_version)
    elif [ -r /etc/redhat-release ]; then
        DISTRO_ID=rhel; DISTRO_NAME=$(sed -n '1p' /etc/redhat-release); DISTRO_VERSION=unknown
    elif [ -r /etc/arch-release ]; then
        DISTRO_ID=arch; DISTRO_NAME='Arch Linux'; DISTRO_VERSION=rolling
    elif [ -r /etc/SuSE-release ]; then
        DISTRO_ID=suse; DISTRO_NAME='SUSE Linux'; DISTRO_VERSION=unknown
    else
        DISTRO_ID=unknown; DISTRO_NAME='Unknown Linux'; DISTRO_VERSION=unknown
    fi
    [ -n "$DISTRO_ID" ] || DISTRO_ID=unknown
    [ -n "$DISTRO_NAME" ] || DISTRO_NAME='Unknown Linux'
    [ -n "$DISTRO_VERSION" ] || DISTRO_VERSION=unknown
    case $DISTRO_ID in
        debian|ubuntu|linuxmint|pop|kali|rhel|rocky|almalinux|centos|ol|fedora|opensuse*|sles|arch|manjaro|endeavouros|alpine|gentoo|devuan|void) COMPATIBILITY_MODE=targeted ;;
        *) COMPATIBILITY_MODE=generic ;;
    esac
}

detect_architecture() {
    DETECTED_ARCH=$(uname -m 2>/dev/null || printf unknown)
    case $DETECTED_ARCH in
        x86_64|amd64) ARCHITECTURE=amd64 ;;
        *) die "当前版本仅支持 Linux amd64/x86_64。检测到架构：$DETECTED_ARCH" ;;
    esac
}

detect_package_manager() {
    PACKAGE_MANAGER=unknown
    for package_candidate in apt-get dnf yum apk zypper pacman emerge xbps-install; do
        if command_exists "$package_candidate"; then
            case $package_candidate in
                apt-get) PACKAGE_MANAGER=apt ;;
                xbps-install) PACKAGE_MANAGER=xbps ;;
                *) PACKAGE_MANAGER=$package_candidate ;;
            esac
            return 0
        fi
    done
}

pkg_update() {
    case $PACKAGE_MANAGER in
        apt) DEBIAN_FRONTEND=noninteractive apt-get update ;;
        dnf) dnf -y makecache ;;
        yum) yum -y makecache ;;
        apk) apk update ;;
        zypper) zypper --non-interactive refresh ;;
        pacman) pacman -Sy --noconfirm ;;
        emerge) emerge --sync ;;
        xbps) xbps-install -S -y ;;
        *) return 1 ;;
    esac
}

pkg_install() {
    package_name=$1
    case $PACKAGE_MANAGER in
        apt) DEBIAN_FRONTEND=noninteractive apt-get install -y "$package_name" ;;
        dnf) dnf install -y "$package_name" ;;
        yum) yum install -y "$package_name" ;;
        apk) apk add --no-cache "$package_name" ;;
        zypper) zypper --non-interactive install "$package_name" ;;
        pacman) pacman -S --noconfirm --needed "$package_name" ;;
        emerge) emerge "$package_name" ;;
        xbps) xbps-install -y "$package_name" ;;
        *) return 1 ;;
    esac
}

pkg_is_installed() {
    package_name=$1
    case $PACKAGE_MANAGER in
        apt) dpkg-query -W "$package_name" >/dev/null 2>&1 ;;
        dnf|yum) rpm -q "$package_name" >/dev/null 2>&1 ;;
        apk) apk info -e "$package_name" >/dev/null 2>&1 ;;
        zypper) rpm -q "$package_name" >/dev/null 2>&1 ;;
        pacman) pacman -Q "$package_name" >/dev/null 2>&1 ;;
        emerge) command_exists qlist && qlist -I "$package_name" >/dev/null 2>&1 ;;
        xbps) xbps-query "$package_name" >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}

detect_container() {
    CONTAINER_TYPE=none
    if [ -f /.dockerenv ]; then CONTAINER_TYPE=docker
    elif [ -f /run/.containerenv ]; then CONTAINER_TYPE=podman
    elif [ -r /proc/1/cgroup ]; then
        for container_candidate in docker podman lxc openvz containerd; do
            if grep "$container_candidate" /proc/1/cgroup >/dev/null 2>&1; then
                CONTAINER_TYPE=$container_candidate
                break
            fi
        done
    fi
}

install_downloader_if_needed() {
    if command_exists curl || command_exists wget; then return 0; fi
    log_info "未找到 curl/wget，正在安装最小下载工具..."
    pkg_update || die "无法刷新软件包索引"
    pkg_install curl || die "无法安装 curl"
}

ensure_ca_bundle() {
    for ca_file in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
        if [ -s "$ca_file" ]; then return 0; fi
    done
    log_info "缺少 CA Bundle，正在安装 ca-certificates..."
    pkg_update || die "无法刷新软件包索引"
    pkg_install ca-certificates || die "无法安装 ca-certificates"
}
