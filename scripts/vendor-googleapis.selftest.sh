#!/usr/bin/env bash
# Self-test of vendor-googleapis.sh's `check`, offline, on a copy of the
# vendored tree: the tree as committed passes; a file whose bytes differ from
# the manifest, a file the manifest names that is missing, a file the
# manifest does not name, and a manifest whose first line pins no commit
# each fail, saying which.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fresh() { # a copy of the script and the vendored tree, at $work/<name>
  local d="$work/$1"
  mkdir -p "$d/scripts" "$d/proto"
  cp "$here/vendor-googleapis.sh" "$d/scripts/"
  cp -R "$here/../proto/third_party" "$d/proto/"
  echo "$d"
}
expect() { # code, dir, label, text the output must hold (or "")
  local got=0 out
  out=$("$2/scripts/vendor-googleapis.sh" check 2>&1) || got=$?
  if [ "$got" != "$1" ]; then echo "selftest FAIL: $3 (exit $got, wanted $1): $out" >&2; exit 1; fi
  if [ -n "$4" ] && ! grep -qF "$4" <<< "$out"; then
    echo "selftest FAIL: $3 does not say '$4': $out" >&2
    exit 1
  fi
}

d=$(fresh clean)
expect 0 "$d" "the vendored tree as committed" "ok   vendor-googleapis"

d=$(fresh tampered)
echo "// edited" >> "$d/proto/third_party/google/api/resource.proto"
expect 1 "$d" "an edited file" "digest differs: google/api/resource.proto"

d=$(fresh missing)
rm "$d/proto/third_party/google/rpc/status.proto"
expect 1 "$d" "a missing file" "missing: google/rpc/status.proto"

d=$(fresh extra)
echo 'syntax = "proto3";' > "$d/proto/third_party/google/api/http.proto"
expect 1 "$d" "a file the manifest does not name" "not in the manifest: google/api/http.proto"

d=$(fresh unpinned)
sed -i.bak '1s/.*/# googleapis commit main/' "$d/proto/third_party/google/SHA256SUMS"
rm "$d/proto/third_party/google/SHA256SUMS.bak"
expect 1 "$d" "a manifest pinning no commit" "is not '# googleapis commit <40 hex>'"

echo "ok   vendor-googleapis selftest"
