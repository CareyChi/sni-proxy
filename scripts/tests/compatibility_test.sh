#!/bin/sh

# Source-only test suite for future Linux/BusyBox validation. This file is
# intentionally not executed during the current code-only stage.
set -u

TEST_DIR=$(CDPATH= cd -P "$(dirname "$0")" 2>/dev/null && pwd -P) || exit 1
SCRIPT_LIB_DIR=$(dirname "$TEST_DIR")/lib
. "$SCRIPT_LIB_DIR/common.sh"

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

is_safe_absolute_dir /opt/sni-proxy || fail 'expected install directory to be safe'
is_safe_absolute_dir /custom/sni-proxy || fail 'expected custom install directory to be safe'
for unsafe_path in '' / /opt /etc /var /usr; do
    if is_safe_absolute_dir "$unsafe_path"; then fail "accepted unsafe path: $unsafe_path"; fi
done

fixture_root=${TMPDIR:-/tmp}/sni-proxy-shell-test.$$
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM
mkdir -p "$fixture_root/opt/sni-proxy/bin" "$fixture_root/usr/local/bin" || exit 1
: > "$fixture_root/opt/sni-proxy/bin/sni-proxyctl"
ln -s ../../../opt/sni-proxy/bin/sni-proxyctl "$fixture_root/usr/local/bin/sni-proxyctl" || exit 1
resolved=$(resolve_symlink "$fixture_root/usr/local/bin/sni-proxyctl") || fail 'relative symlink did not resolve'
[ "$resolved" = "$fixture_root/opt/sni-proxy/bin/sni-proxyctl" ] || fail "unexpected resolution: $resolved"

printf 'PASS\n'
