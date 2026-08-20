#!/bin/sh

# Source-only safety checks executed by CI and usable under BusyBox sh.
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
for unsafe_path in /var/../etc /opt/foo/../../etc /opt//sni-proxy /opt/sni-proxy/; do
    if is_safe_absolute_dir "$unsafe_path"; then fail "accepted non-canonical path: $unsafe_path"; fi
done
canonical_managed_dir install /opt/sni-proxy >/dev/null || fail 'expected managed install directory to be accepted'
if canonical_managed_dir install /etc/sni-proxy >/dev/null; then fail 'accepted install directory in config space'; fi
paths_overlap /opt/sni-proxy /opt/sni-proxy/config || fail 'failed to detect nested directories'
if paths_overlap /opt/sni-proxy /etc/sni-proxy; then fail 'reported disjoint directories as overlapping'; fi

fixture_root=${TMPDIR:-/tmp}/sni-proxy-shell-test.$$
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM
mkdir -p "$fixture_root/opt/sni-proxy/bin" "$fixture_root/usr/local/bin" || exit 1
: > "$fixture_root/opt/sni-proxy/bin/sni-proxyctl"
ln -s ../../../opt/sni-proxy/bin/sni-proxyctl "$fixture_root/usr/local/bin/sni-proxyctl" || exit 1
if [ -L "$fixture_root/usr/local/bin/sni-proxyctl" ]; then
    resolved=$(resolve_symlink "$fixture_root/usr/local/bin/sni-proxyctl") || fail 'relative symlink did not resolve'
    [ "$resolved" = "$fixture_root/opt/sni-proxy/bin/sni-proxyctl" ] || fail "unexpected resolution: $resolved"
fi

if [ "$(id -u)" -eq 0 ]; then
    marker_dir=$fixture_root/marker
    mkdir -p "$marker_dir" || exit 1
    printf '{"installation_id":"fixture-id","role":"install"}\n' > "$marker_dir/.sni-proxy-managed"
    chown root:root "$marker_dir/.sni-proxy-managed" || exit 1
    chmod 644 "$marker_dir/.sni-proxy-managed" || exit 1
    validate_managed_marker "$marker_dir" install fixture-id || fail 'valid root-owned marker was rejected'
    mv "$marker_dir/.sni-proxy-managed" "$marker_dir/real-marker" || exit 1
    ln -s real-marker "$marker_dir/.sni-proxy-managed" || exit 1
    if validate_managed_marker "$marker_dir" install fixture-id; then
        fail 'symbolic-link marker was accepted'
    fi
fi

printf 'PASS\n'
