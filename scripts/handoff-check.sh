#!/usr/bin/env bash
# No in-memory hand-offs between components (owner ruling 2026-11-25: aggregates,
# projectors and runners communicate exclusively by messages, as if in different
# processes). In crates/core/application and crates/root, refuse a struct field
# or static that is
#   - a lock around a collection (Mutex|RwLock of BTreeMap|HashMap|Vec|VecDeque),
#   - a Condvar,
#   - a channel sender carrying a value (Sender<T>, T not `()`), or
#   - a boxed or shared closure (Box|Arc|Rc of `dyn Fn*`), or a trait method
#     whose signature has one,
# unless scripts/handoff-allow.txt lists it with why. The rules are ast-grep's
# (scripts/handoff-rules/); `*.test.rs` files are not scanned. The check is
# heuristic by design: it errs loud, and the allow list is where each remaining
# case says whether it is a runner's own state (legitimate) or debt naming the
# pattern that replaces it.
#
# An allow entry is `<file> | <text of the declaration> | <why>`: it covers every
# hit in that file whose declaration text (whitespace collapsed) contains the
# fragment. An entry that covers nothing fails too, so the list only shrinks.
# A `why` that starts `debt:` is counted and printed: the list of what is left to
# remove; it must end naming the phase that removes it (P3, P4 or P5; never P2). Anything else is a runner's own state and says why it is not a
# hand-off.
#
# Usage: handoff-check.sh [root]   (root defaults to the repository)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
top=${1:-$(git rev-parse --show-toplevel)}
cd "$top"
allow_file=scripts/handoff-allow.txt
config="$here/handoff-rules/sgconfig.yml"

command -v ast-grep >/dev/null 2>&1 || {
  echo "FAIL lint-handoffs: ast-grep is not on PATH (cargo install ast-grep)" >&2
  exit 1
}

files=() fragments=() whys=()
if [ -f "$allow_file" ]; then
  while IFS= read -r entry; do
    [[ -z ${entry//[[:space:]]/} || $entry == \#* ]] && continue
    IFS='|' read -r file fragment why <<< "$entry"
    why=${why:-}
    for v in file fragment why; do
      t=${!v}
      t=${t#"${t%%[![:space:]]*}"}
      t=${t%"${t##*[![:space:]]}"}
      printf -v "$v" '%s' "$t"
    done
    if [ -z "$file" ] || [ -z "$fragment" ] || [ -z "$why" ]; then
      echo "FAIL lint-handoffs: $allow_file: an entry needs a file, the text of the declaration and why: $entry" >&2
      exit 1
    fi
    # A debt entry names the phase that removes it, P3, P4 or P5 (owner ruling
    # 2026-11-26: no entry outlives its phase, and P2 removes its own rows, so
    # an entry naming P2 or no phase is refused).
    if [[ $why == debt:* ]]; then
      phase=$(printf '%s' "$why" | grep -oE '\bP[0-9]+\b' | tail -n 1 || true)
      case "$phase" in
        P3 | P4 | P5) ;;
        *)
          echo "FAIL lint-handoffs: $allow_file: a debt entry must name the phase that removes it (P3, P4 or P5), not '${phase:-none}': $entry" >&2
          exit 1
          ;;
      esac
    fi
    files+=("$file") fragments+=("$fragment") whys+=("$why")
  done < "$allow_file"
fi
used=()
for _ in "${files[@]+"${files[@]}"}"; do used+=(0); done

paths=()
for p in crates/core/application crates/root; do [ -d "$p" ] && paths+=("$p"); done
[ "${#paths[@]}" -gt 0 ] || { echo "ok   lint-handoffs (nothing to scan)"; exit 0; }

found=0 debt=0 runner=0
while IFS=$'\t' read -r rule file line text; do
  [ -n "$rule" ] || continue
  allowed=0
  for i in "${!files[@]}"; do
    if [[ $file == "${files[$i]}" && $text == *"${fragments[$i]}"* ]]; then
      if [ "$allowed" = 0 ]; then
        if [[ ${whys[$i]} == debt:* ]]; then debt=$((debt + 1)); else runner=$((runner + 1)); fi
      fi
      allowed=1 used[i]=1
    fi
  done
  [ "$allowed" = 1 ] && continue
  echo "$file:$line: $rule: $text"
  found=$((found + 1))
done < <(ast-grep scan --config "$config" --json=stream "${paths[@]}" 2>/dev/null \
  | jq -r '[.ruleId, .file, (.range.start.line + 1), (.text | gsub("\\s+"; " "))] | @tsv' | sort -t$'\t' -k2,2 -k3,3n)

stale=0
for i in "${!files[@]}"; do
  if [ "${used[$i]}" = 0 ]; then
    echo "FAIL lint-handoffs: stale entry in $allow_file (it covers nothing): ${files[$i]} | ${fragments[$i]}" >&2
    stale=$((stale + 1))
  fi
done

if [ "$found" -gt 0 ]; then
  echo "FAIL lint-handoffs: $found in-memory hand-off(s) between components; replace each with a message (a command, fact or event on a stream), or list it in $allow_file with why" >&2
  exit 1
fi
[ "$stale" -eq 0 ] || exit 1
echo "ok   lint-handoffs ($debt debt, $runner runner state, listed in $allow_file)"
