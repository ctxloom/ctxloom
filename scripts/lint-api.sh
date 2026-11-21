#!/usr/bin/env bash
# api-linter over the daemon's API protos (proto/ctxloom/v1; owner rulings
# S36/S37/S57, D6; step 10 U1), at ONE pinned release, its archive checked by
# SHA-256 before it is unpacked. Linux x86_64 only (D6: Linux CI runs it);
# elsewhere it says so and fails, never passes unrun.
#
# The import paths: proto/ (ctxloom's), proto/third_party (the vendored
# googleapis, checked against its manifest first: scripts/vendor-googleapis.sh
# check) and angzarr's own protos at the commit Cargo.lock pins (the
# angzarr-proto package's checkout, found by `cargo metadata`): its
# io/angzarr/v1/options.proto. The linter's config is proto/api-linter.yaml.
#
# The binary is cached at ~/.cache/ctxloom-api-linter/<version>/api-linter,
# and the archive is checked again whenever it is fetched. Exit 0 no
# finding, 1 a finding (or a check that failed), 2 usage.
set -euo pipefail

version=2.4.1
archive_sha256=adf6b94b01bf1d82723feb2790109b69fc74af65f9be334e418dcc9b9d9ddd03
url="https://github.com/googleapis/api-linter/releases/download/v$version/api-linter-$version-linux-amd64.tar.gz"

top=$(cd "$(dirname "$0")/.." && pwd)
[ $# -eq 0 ] || { echo "usage: lint-api.sh" >&2; exit 2; }

if [ "$(uname -s)-$(uname -m)" != Linux-x86_64 ]; then
    echo "FAIL lint-api: api-linter $version is pinned for linux x86_64 only, not $(uname -s)-$(uname -m)" >&2
    exit 1
fi

"$top/scripts/vendor-googleapis.sh" check >/dev/null

cache="${XDG_CACHE_HOME:-$HOME/.cache}/ctxloom-api-linter/$version"
linter="$cache/api-linter"
if [ ! -x "$linter" ]; then
    mkdir -p "$cache"
    stage=$(mktemp -d "$cache/fetch.XXXXXX")
    trap 'rm -rf "$stage"' EXIT
    curl -fsSL --proto '=https' --tlsv1.2 -o "$stage/api-linter.tar.gz" "$url"
    got=$(sha256sum "$stage/api-linter.tar.gz" | cut -d' ' -f1)
    if [ "$got" != "$archive_sha256" ]; then
        echo "FAIL lint-api: api-linter $version's archive is $got, not the pinned $archive_sha256" >&2
        exit 1
    fi
    tar -xzf "$stage/api-linter.tar.gz" -C "$stage" api-linter
    mv "$stage/api-linter" "$linter"
fi
if [ "$("$linter" --version)" != "api-linter $version" ]; then
    echo "FAIL lint-api: $linter is not api-linter $version" >&2
    exit 1
fi

# angzarr's protos, at the commit the lock pins.
manifest=$(cargo metadata --format-version 1 --offline --manifest-path "$top/Cargo.toml" |
    jq -r '.packages[] | select(.name == "angzarr-proto") | .manifest_path' | head -n 1)
angzarr="$(dirname "$(dirname "$(dirname "$manifest")")")/angzarr-project/proto"
if [ ! -f "$angzarr/io/angzarr/v1/options.proto" ]; then
    echo "FAIL lint-api: angzarr's options.proto is not under $angzarr" >&2
    exit 1
fi

cd "$top/proto"
exec "$linter" --set-exit-status --config api-linter.yaml --output-format yaml \
    -I . -I third_party -I "$angzarr" ctxloom/v1/*.proto
