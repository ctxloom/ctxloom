#!/usr/bin/env bash
# The vendored googleapis protos (owner rulings S36/S37/S57, D6; step 10 U1):
# the files of proto/third_party/google/SHA256SUMS, at the ONE googleapis
# commit its first line pins, each checked against its SHA-256 there.
#
#   vendor-googleapis.sh check   the files on disk match the manifest (no
#                                network); `just lint-api` runs it first
#   vendor-googleapis.sh fetch   fetch each file of the manifest at the
#                                pinned commit, refuse one whose digest is
#                                not the manifest's, then write them all
#
# The manifest is the one place the commit and the digests are spelled. To
# move the pin: change the commit and the digests together, by hand, after
# reading what changed upstream; `fetch` never rewrites the manifest.
#
# angzarr's google/api/annotations.proto and http.proto are not vendored:
# they are angzarr's own. Exit 0 ok, 1 a mismatch or a failed fetch, 2 usage.
set -euo pipefail

top=$(cd "$(dirname "$0")/.." && pwd)
# The include root: a vendored file's path below it is its import path.
dir="$top/proto/third_party"
manifest="$dir/google/SHA256SUMS"

commit=$(sed -n '1s/^# googleapis commit \([0-9a-f]\{40\}\)$/\1/p' "$manifest")
if [ -z "$commit" ]; then
    echo "FAIL vendor-googleapis: $manifest's first line is not '# googleapis commit <40 hex>'" >&2
    exit 1
fi

# Each `<sha256>  <path>` line of the manifest, the path relative to
# proto/third_party (its import path); `google/LICENSE` is googleapis' own
# LICENSE, at the root of its repository.
entries() { grep -E '^[0-9a-f]{64}  [A-Za-z0-9_./-]+$' "$manifest"; }

check() {
    local root=$1 bad=0
    while read -r sum path; do
        if [ ! -f "$root/$path" ]; then
            echo "missing: $path"
            bad=1
            continue
        fi
        got=$(sha256sum "$root/$path" | cut -d' ' -f1)
        if [ "$got" != "$sum" ]; then
            echo "digest differs: $path (manifest $sum, file $got)"
            bad=1
        fi
    done < <(entries)
    # Nothing vendored that the manifest does not name.
    while IFS= read -r file; do
        rel=${file#"$root"/}
        if ! entries | cut -d' ' -f3 | grep -qxF "$rel"; then
            echo "not in the manifest: $rel"
            bad=1
        fi
    done < <(find "$root" -type f ! -path "$root/google/SHA256SUMS" | sort)
    return "$bad"
}

case "${1:-}" in
    check)
        if ! out=$(check "$dir"); then
            echo "FAIL vendor-googleapis: the vendored files are not the manifest's (googleapis $commit)" >&2
            printf '%s\n' "$out" >&2
            exit 1
        fi
        echo "ok   vendor-googleapis ($(entries | wc -l) files at googleapis ${commit:0:10})"
        ;;
    fetch)
        stage=$(mktemp -d "${TMPDIR:-/tmp}/vendor-googleapis.XXXXXX")
        trap 'rm -rf "$stage"' EXIT
        while read -r _ path; do
            upstream=$path
            [ "$path" = google/LICENSE ] && upstream=LICENSE
            mkdir -p "$stage/$(dirname "$path")"
            curl -fsSL --proto '=https' --tlsv1.2 -o "$stage/$path" \
                "https://raw.githubusercontent.com/googleapis/googleapis/$commit/$upstream"
        done < <(entries)
        if ! out=$(check "$stage"); then
            echo "FAIL vendor-googleapis: googleapis $commit does not match the manifest; nothing written" >&2
            printf '%s\n' "$out" >&2
            exit 1
        fi
        while read -r _ path; do
            mkdir -p "$dir/$(dirname "$path")"
            cp "$stage/$path" "$dir/$path"
        done < <(entries)
        echo "ok   vendor-googleapis fetched $(entries | wc -l) files at googleapis ${commit:0:10}"
        ;;
    *)
        echo "usage: vendor-googleapis.sh check|fetch" >&2
        exit 2
        ;;
esac
