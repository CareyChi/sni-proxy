#!/bin/sh

# Assemble an already-built static linux/amd64 binary into the single release
# layout consumed by install.sh and the Go updater. This script does not build.
set -u

SCRIPT_DIR=$(CDPATH= cd -P "$(dirname "$0")" 2>/dev/null && pwd -P) || exit 1
PROJECT_DIR=$(dirname "$SCRIPT_DIR")
SOURCE_BINARY=${1:-$PROJECT_DIR/bin/sni-proxy}
OUTPUT_DIR=${2:-$PROJECT_DIR/dist}
SIGNING_KEY_FILE=${SNI_PROXY_SIGNING_KEY_FILE:-}
mkdir -p "$OUTPUT_DIR" || exit 1
OUTPUT_DIR=$(CDPATH= cd "$OUTPUT_DIR" 2>/dev/null && pwd -P) || exit 1
STAGE_DIR=$OUTPUT_DIR/sni-proxy-linux-amd64

[ -f "$SOURCE_BINARY" ] || { printf '缺少预编译 linux/amd64 二进制：%s\n' "$SOURCE_BINARY" >&2; exit 1; }
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR/bin" "$STAGE_DIR/libexec" "$STAGE_DIR/service/systemd" "$STAGE_DIR/service/openrc" \
    "$STAGE_DIR/service/sysvinit" "$STAGE_DIR/service/runit/sni-proxy" "$STAGE_DIR/templates" || exit 1

cp "$SOURCE_BINARY" "$STAGE_DIR/bin/sni-proxy" || exit 1
cp "$SCRIPT_DIR/sni-proxyctl" "$STAGE_DIR/bin/sni-proxyctl" || exit 1
cp "$SCRIPT_DIR/install.sh" "$STAGE_DIR/install.sh" || exit 1
cp "$SCRIPT_DIR/install.sh" "$STAGE_DIR/libexec/install.sh" || exit 1
for library in common.sh distro.sh service.sh systemd.sh openrc.sh sysv.sh runit.sh network.sh update.sh uninstall.sh; do
    cp "$SCRIPT_DIR/lib/$library" "$STAGE_DIR/libexec/$library" || exit 1
done
cp "$PROJECT_DIR/service/systemd/sni-proxy.service" "$STAGE_DIR/service/systemd/sni-proxy.service" || exit 1
cp "$PROJECT_DIR/service/openrc/sni-proxy" "$STAGE_DIR/service/openrc/sni-proxy" || exit 1
cp "$PROJECT_DIR/service/sysvinit/sni-proxy" "$STAGE_DIR/service/sysvinit/sni-proxy" || exit 1
cp "$PROJECT_DIR/service/runit/sni-proxy/run" "$STAGE_DIR/service/runit/sni-proxy/run" || exit 1
cp "$PROJECT_DIR/VERSION" "$STAGE_DIR/VERSION" || exit 1
cp "$PROJECT_DIR/templates/README.md" "$STAGE_DIR/templates/README.md" || exit 1
chmod 755 "$STAGE_DIR/bin/sni-proxy" "$STAGE_DIR/bin/sni-proxyctl" "$STAGE_DIR/install.sh" "$STAGE_DIR/libexec"/*.sh \
    "$STAGE_DIR/service/openrc/sni-proxy" "$STAGE_DIR/service/sysvinit/sni-proxy" "$STAGE_DIR/service/runit/sni-proxy/run"

archive=$OUTPUT_DIR/sni-proxy-linux-amd64.tar.gz
(CDPATH= cd "$STAGE_DIR" && tar -czf "$archive" .) || exit 1
if command -v sha256sum >/dev/null 2>&1; then
    (CDPATH= cd "$OUTPUT_DIR" && sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256")
elif command -v shasum >/dev/null 2>&1; then
    (CDPATH= cd "$OUTPUT_DIR" && shasum -a 256 "$(basename "$archive")" > "$(basename "$archive").sha256")
else
    printf '缺少 sha256sum/shasum，无法生成校验文件\n' >&2
    exit 1
fi
command -v openssl >/dev/null 2>&1 || { printf '缺少 openssl，无法生成 Ed25519 签名\n' >&2; exit 1; }
[ -f "$SIGNING_KEY_FILE" ] || { printf '必须通过 SNI_PROXY_SIGNING_KEY_FILE 提供 Ed25519 私钥\n' >&2; exit 1; }
archive_name=$(basename "$archive")
archive_sha=$(awk 'NR == 1 { print $1 }' "$archive.sha256")
release_version=$(sed -n '1p' "$PROJECT_DIR/VERSION" | tr -d '\r\n')
manifest=$OUTPUT_DIR/sni-proxy-linux-amd64.manifest.json
signature=$manifest.sig
printf '{"version":"%s","architecture":"amd64","archive":"%s","sha256":"%s"}\n' \
    "$release_version" "$archive_name" "$archive_sha" > "$manifest" || exit 1
openssl pkeyutl -sign -rawin -inkey "$SIGNING_KEY_FILE" -in "$manifest" -out "$signature.bin" || exit 1
openssl base64 -A -in "$signature.bin" > "$signature" || exit 1
rm -f "$signature.bin"
printf 'Release layout ready: %s\n' "$archive"
