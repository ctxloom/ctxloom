#!/usr/bin/env bash
# Refuses a bare `$HOME` that locates user-wide state in gates/, scripts/ and
# the justfile (2026-11-27: the machine-wide test lock keyed on $HOME was one
# file per session home, so it locked nothing across sessions). Comment lines
# and *.selftest.sh fixtures are not judged; each kept use is in
# scripts/home-allow.txt with its reason, and an entry that matches nothing
# fails (a stale allowance). The right resolution is gates/account-cache.sh.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
top=$(cd "$here/.." && pwd)
allow=$here/home-allow.txt
fail() { echo "FAIL home-guard.selftest: $*" >&2; exit 1; }
used=$(mktemp)
trap 'rm -f "$used"' EXIT
bad=
while IFS= read -r hit; do
    file=${hit%%:*}; rest=${hit#*:}; text=${rest#*:}
    trimmed=${text#"${text%%[![:space:]]*}"}
    case "$trimmed" in '#'*) continue ;; esac
    ok=
    while IFS='|' read -r f frag _why; do
        case "$f" in ''|'#'*) continue ;; esac
        if [ "$f" = "$file" ] && [[ $text == *"$frag"* ]]; then ok=1; echo "$f|$frag" >> "$used"; fi
    done < "$allow"
    [ -n "$ok" ] || bad+="  $file:$rest"$'\n'
done < <(cd "$top" && grep -rnE '\$HOME|\$\{HOME' gates scripts justfile \
    --exclude='*.selftest.sh' --exclude=home-allow.txt --exclude=MIGRATING.md || true)
[ -z "$bad" ] || fail "a bare \$HOME locates user-wide state (use gates/account-cache.sh, or allow it with a reason in scripts/home-allow.txt):"$'\n'"$bad"
while IFS='|' read -r f frag _why; do
    case "$f" in ''|'#'*) continue ;; esac
    grep -qxF "$f|$frag" "$used" || fail "stale allowance, it matches no line: $f|$frag"
done < "$allow"
echo "ok   home-guard.selftest"
