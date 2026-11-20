#!/usr/bin/env bash
# No dependency reaches outside the repository by path. A `path = "..."` in a
# dependency table ([dependencies], [dev-dependencies], [build-dependencies],
# [workspace.dependencies], [target.*.*dependencies], [patch.*], [replace]) of
# a tracked Cargo.toml or .cargo/*.toml, or a `paths = [...]` override, that is
# absolute or climbs out of the repository root once normalized, is refused: a
# path to a sibling checkout builds on one machine only. Local co-development
# is the gitignored .cargo/config.toml (copied from .cargo/local-deps.toml);
# a tracked .cargo/config.toml or .cargo/config is refused too.
#
# It judges the INDEX (the staged content, or the commit once committed), never
# the working copy. Paths in a Cargo.toml are relative to its directory; those
# in a .cargo file are relative to the directory holding .cargo (cargo's rule).
# .cargo/local-deps.toml is the sample cargo never reads, and is exempt.
#
# Limits: a symlink inside the repository that points outside is not followed;
# a path assembled any other way than a TOML string is not seen; a line number
# is found by text search of the path's value, so it is the first such line.
#
# Usage: path-deps-check.sh [root]   (root defaults to the repository)
# Silent on a pass; on a failure the first line is the summary, then a line
# per finding; exit 1.
set -euo pipefail
top=${1:-$(git rev-parse --show-toplevel)}
exec python3 -I -c '
import posixpath, subprocess, sys, tomllib

root = sys.argv[1]
FIX = ("use the git dependency pinned by full commit; keep local paths in the "
       "gitignored .cargo/config.toml")

def git(*args):
    return subprocess.run(["git", "-C", root, *args], check=True,
                          capture_output=True).stdout

staged = git("ls-files", "-z", "--stage").decode().split("\0")
entries = {}
for rec in staged:
    if not rec:
        continue
    meta, path = rec.split("\t", 1)
    mode, blob, _ = meta.split()
    if mode in ("100644", "100755"):
        entries[path] = blob

findings = []

def is_cargo_config(path):
    parts = path.split("/")
    return (len(parts) >= 2 and parts[-2] == ".cargo"
            and (parts[-1] in ("config", "config.toml")))

def outside(base, value):
    if not isinstance(value, str):
        return False
    if value.startswith("/") or value.startswith("\\") or value.startswith("~") \
            or (len(value) > 1 and value[1] == ":"):
        return True
    joined = posixpath.normpath(posixpath.join(base, value))
    return joined == ".." or joined.startswith("../")

def dep_tables(doc):
    for key in ("dependencies", "dev-dependencies", "build-dependencies"):
        if isinstance(doc.get(key), dict):
            yield key, doc[key]
    ws = doc.get("workspace")
    if isinstance(ws, dict) and isinstance(ws.get("dependencies"), dict):
        yield "workspace.dependencies", ws["dependencies"]
    tg = doc.get("target")
    if isinstance(tg, dict):
        for cfg, sub in tg.items():
            if isinstance(sub, dict):
                for name, t in dep_tables(sub):
                    yield "target." + cfg + "." + name, t
    pt = doc.get("patch")
    if isinstance(pt, dict):
        for src, t in pt.items():
            if isinstance(t, dict):
                yield "patch." + src, t
    if isinstance(doc.get("replace"), dict):
        yield "replace", doc["replace"]

def line_of(text, value):
    for n, line in enumerate(text.splitlines(), 1):
        s = line.lstrip()
        if s.startswith("#"):
            continue
        if ("\"" + value + "\"") in line or ("\x27" + value + "\x27") in line:
            return n
    return 0

def report(path, text, table, name, value):
    n = line_of(text, value)
    findings.append("%s:%s: [%s] %s -> %s" % (path, n or "?", table, name, value))

for path, blob in sorted(entries.items()):
    base_name = path.rsplit("/", 1)[-1]
    if is_cargo_config(path):
        findings.append("%s: a cargo config is local-only and is never tracked" % path)
    is_manifest = base_name == "Cargo.toml"
    in_cargo_dir = "/.cargo/" in "/" + path and path.endswith(".toml") \
        and base_name != "local-deps.toml" and ".cargo" in path.split("/")[:-1]
    if not (is_manifest or in_cargo_dir):
        continue
    text = subprocess.run(["git", "-C", root, "cat-file", "blob", blob], check=True,
                          capture_output=True).stdout.decode("utf-8", "replace")
    try:
        doc = tomllib.loads(text)
    except tomllib.TOMLDecodeError as e:
        findings.append("%s: does not parse (%s); it cannot be checked" % (path, e))
        continue
    d = posixpath.dirname(path)
    base = d if is_manifest else posixpath.dirname(d)
    for table, deps in dep_tables(doc):
        for name, dep in deps.items():
            if isinstance(dep, dict) and "path" in dep and outside(base, dep["path"]):
                report(path, text, table, name, dep["path"])
    if not is_manifest and isinstance(doc.get("paths"), list):
        for p in doc["paths"]:
            if outside(base, p):
                report(path, text, "paths", "paths", p)

if findings:
    print("%d path findings: a dependency path reaches outside the repository, or a local-only cargo config is tracked" % len(findings))
    for f in findings[:38]:
        print(f)
    if len(findings) > 38:
        print("+%d more, see the log" % (len(findings) - 38))
    print("fix: " + FIX)
    sys.exit(1)
' "$top"
