#!/usr/bin/env bash
# Projectors never communicate with each other (architecture rule 2): a
# projector's state and handler hold no other projector's handle or read model
# and wait on none. Reusing another read model's pure fold functions or value
# types is allowed.
#
# A projector is any type with `impl Model for T` (held-projection) or
# `impl ProjectorHandler for T` (angzarr), a handle a tuple struct around a
# `Held<..>`; both are discovered by ast-grep (scripts/projector-rules/) in
# crates/, so a new projector is covered without a list. The owner of a name is
# its crate (crates/<ring>/<role>/<crate>). In every non-test file that defines
# a projector, a name owned by ANOTHER crate (a handle, a model type or a
# handler type, in a field, parameter, body, import or alias) is a finding, and
# so is a glob import from another projector's crate (it would hide the name).
# A type that is no projector (a pure fold such as session_index::Projection, a
# value type) is never in the set, so reuse of it passes. Whether it is a
# projector's handle that is held is decided by name, a heuristic that errs loud.
#
# An allow entry is `<file> | <text of the hit> | <why>` and covers every hit in
# that file whose text contains the fragment. An entry that covers nothing fails
# too. A `why` that starts `PENDING` is an open question for the owner: counted
# and a note line is printed for each, so the debt stays visible.
#
# Usage: projector-check.sh [root]   (root defaults to the repository)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
top=${1:-$(git rev-parse --show-toplevel)}
cd "$top"
allow_file=scripts/projector-allow.txt
config="$here/projector-rules/sgconfig.yml"

command -v ast-grep >/dev/null 2>&1 || {
  echo "FAIL lint-projectors: ast-grep is not on PATH (cargo install ast-grep)" >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || { echo "FAIL lint-projectors: jq is not on PATH" >&2; exit 1; }
[ -d crates ] || { echo "ok   lint-projectors (nothing to scan)"; exit 0; }

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
      echo "FAIL lint-projectors: $allow_file: an entry needs a file, the text of the hit and why: $entry" >&2
      exit 1
    fi
    files+=("$file") fragments+=("$fragment") whys+=("$why")
  done < "$allow_file"
fi
used=()
for _ in "${files[@]+"${files[@]}"}"; do used+=(0); done

crate_of() { echo "$1" | cut -d/ -f1-4; }

# Discovery: "<file>\t<type>" per projector, "<file>\t<type>" per handle.
discover() { # rule
  ast-grep scan --config "$config" --filter "^$1\$" --json=stream crates \
    | jq -r '[.file, (.text | gsub("\\s+"; " "))] | @tsv'
}
projector_rows=$(discover projector-impl | while IFS=$'\t' read -r file text; do
  header=${text%%\{*}
  if [[ $header =~ [[:space:]]for[[:space:]]+([A-Za-z0-9_:]*::)?([A-Za-z_][A-Za-z0-9_]*) ]]; then
    printf '%s\t%s\n' "$file" "${BASH_REMATCH[2]}"
  fi
done)
handle_rows=$(discover projector-handle | while IFS=$'\t' read -r file text; do
  if [[ $text =~ struct[[:space:]]+([A-Za-z_][A-Za-z0-9_]*) ]]; then
    printf '%s\t%s\n' "$file" "${BASH_REMATCH[1]}"
  fi
  # the model a handle wraps
  if [[ $text =~ Held\<([A-Za-z0-9_:]*::)?([A-Za-z_][A-Za-z0-9_]*) ]]; then
    printf '%s\t%s\n' "$file" "${BASH_REMATCH[2]}"
  fi
done)

# held-projection is the framework every projector is built on (Held, Model,
# Fill), not a projector: its types are no other projector's.
framework=crates/core/read-model/held-projection
owned=$(printf '%s\n%s\n' "$projector_rows" "$handle_rows" | grep . | grep -v "^$framework/" | while IFS=$'\t' read -r file name; do
  printf '%s\t%s\n' "$(crate_of "$file")" "$name"
done | sort -u)
[ -n "$owned" ] || { echo "ok   lint-projectors (no projector found)"; exit 0; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/rules"
printf 'ruleDirs:\n  - rules\n' > "$work/sgconfig.yml"

crate_lib() { basename "$1" | tr - _; }

found=0 pending=0 reasoned=0
hits=$(printf '%s\n' "$projector_rows" | grep . | cut -f1 | sort -u | while IFS= read -r file; do
  c=$(crate_of "$file")
  names=$(printf '%s\n' "$owned" | awk -F'\t' -v c="$c" '$1 != c {print $2}' | sort -u | paste -sd'|')
  libs=$(printf '%s\n' "$owned" | awk -F'\t' -v c="$c" '$1 != c {print $1}' | sort -u | while IFS= read -r x; do crate_lib "$x"; done | paste -sd'|')
  # A name the crate defines itself (its own `Pending`) is its own, whatever
  # another crate's projector is called.
  local_defs=$(grep -rhoE '\b(struct|enum|type|trait|union)[[:space:]]+[A-Za-z_][A-Za-z0-9_]*' --include='*.rs' --exclude='*.test.rs' "$c" 2>/dev/null \
    | awk '{print $2}' | sort -u || true)
  if [ -n "$local_defs" ]; then
    names=$(printf '%s\n' "$names" | tr '|' '\n' | grep -vxF "$local_defs" | paste -sd'|' || true)
  fi
  [ -n "$names" ] || continue
  cat > "$work/rules/foreign.yml" <<RULE
id: projector-foreign
language: rust
severity: hint
message: A projector names another projector.
rule:
  any:
    - kind: type_identifier
      regex: '^($names)\$'
      not:
        # Enum::Variant (a capitalised path) is a variant or associated type
        inside:
          kind: scoped_type_identifier
          has:
            field: path
            regex: '^[A-Z]'
    - kind: identifier
      regex: '^($names)\$'
      inside:
        kind: use_declaration
        stopBy: end
    - kind: identifier
      regex: '^($names)\$'
      inside:
        kind: scoped_identifier
        field: path
    - kind: use_wildcard
      regex: '^($libs)::\*\$'
RULE
  ast-grep scan --config "$work/sgconfig.yml" --json=stream "$file" 2>"$work/err" \
    | jq -r '[.file, (.range.start.line + 1), (.text | gsub("\\s+"; " "))] | @tsv'
  if [ -s "$work/err" ]; then
    echo "FAIL lint-projectors: ast-grep: $(head -c 300 "$work/err")" >&2
    exit 1
  fi
done | sort -u) || exit 1

while IFS=$'\t' read -r file line text; do
  [ -n "$file" ] || continue
  allowed=0
  for i in "${!files[@]}"; do
    if [[ $file == "${files[$i]}" && $text == *"${fragments[$i]}"* ]]; then
      allowed=1 used[i]=1
    fi
  done
  [ "$allowed" = 1 ] && continue
  echo "$file:$line: projector-foreign: names another projector: $text"
  found=$((found + 1))
done <<< "$hits"

stale=0
for i in "${!files[@]}"; do
  if [ "${used[$i]}" = 0 ]; then
    echo "FAIL lint-projectors: stale entry in $allow_file (it covers nothing): ${files[$i]} | ${fragments[$i]}" >&2
    stale=$((stale + 1))
  elif [[ ${whys[$i]} == PENDING* ]]; then
    echo "note lint-projectors: ${whys[$i]} (${files[$i]} | ${fragments[$i]})"
    pending=$((pending + 1))
  else
    reasoned=$((reasoned + 1))
  fi
done

if [ "$found" -gt 0 ]; then
  echo "FAIL lint-projectors: $found place(s) where a projector holds or names another projector; fold the events that carry the data instead, or list it in $allow_file with why" >&2
  exit 1
fi
[ "$stale" -eq 0 ] || exit 1
echo "ok   lint-projectors ($pending pending, $reasoned reasoned, listed in $allow_file)"
