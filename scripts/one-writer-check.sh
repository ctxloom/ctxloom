#!/usr/bin/env bash
# The daemon is the store's one writer (owner ruling 2026-11-25). clippy's
# `disallowed-methods` (clippy.toml, crates/core/clippy.toml) refuses an
# angzarr kernel or kernel host started, and the store opened. A site that is
# allowed anyway (`#[expect(clippy::disallowed_methods, ...)]`) is the
# daemon's own composition, and so lives in `crates/root/daemon/ctxloom-daemon`
# (or in the store adapter that opens it, `crates/adapters/store`); or it is a
# single test function that needs a kernel or store of its own
# (`#[allow(clippy::disallowed_methods, reason = "a test kernel or store of its
# own")]`), or a test double's in-memory kernel that never reaches the store
# (its reason says so). Anywhere else it fails, and so does any leftover "P2 one-writer
# debt": there is no ceiling and no allowance for legacy code.
#
# Usage: one-writer-check.sh [root]   (root defaults to the repository)
set -euo pipefail
top=${1:-$(git rev-parse --show-toplevel)}
cd "$top"
[ -d crates ] || { echo "ok   lint-one-writer (no crates)"; exit 0; }
python3 -I - "$top" <<'PY'
import os, re, sys
DAEMON = ("crates/root/daemon/ctxloom-daemon/", "crates/adapters/store/")
TEST_REASON = "a test kernel or store of its own"
FAKE_REASON = "a test fake's own in-memory kernel, never the store"
bad, daemon, tests = [], 0, 0
for dirpath, _, names in os.walk("crates"):
    for name in names:
        if not name.endswith(".rs"):
            continue
        path = os.path.join(dirpath, name).replace(os.sep, "/")
        lines = open(path, encoding="utf-8", errors="replace").read().split("\n")
        for i, line in enumerate(lines):
            if "P2 one-writer debt" in line:
                bad.append(f"{path}:{i + 1}: a leftover one-writer debt reason")
                continue
            if "clippy::disallowed_methods" not in line or line.lstrip().startswith("//"):
                continue
            window = "\n".join(lines[max(0, i - 1): i + 4])
            if not re.search(r"#\[(expect|allow)\(", window):
                continue
            if path.startswith(DAEMON):
                daemon += 1
            elif ("#[allow(" in window and TEST_REASON in window) or FAKE_REASON in window:
                tests += 1
            else:
                bad.append(f"{path}:{i + 1}: a write to the store outside the daemon")
if bad:
    print("FAIL lint-one-writer: " + str(len(bad)) + " site(s) outside the daemon: send a command or fact to its coordinator instead", file=sys.stderr)
    for b in bad[:40]:
        print("  " + b, file=sys.stderr)
    sys.exit(1)
print(f"ok   lint-one-writer ({daemon} daemon sites, {tests} per-test allowances)")
PY
