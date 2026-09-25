#!/bin/sh
# Run with cwd = an ooze laboratory (every file a symlink back to the real
# checkout). Replaces the laboratory's symlink with a real copy for EVERY file
# any package in the module embeds, including its tests' embeds.
#
# go:embed refuses to embed a symlink ("cannot embed irregular file" / "contains
# no embeddable files"), so without this no package that embeds anything builds
# in the laboratory — and a laboratory that cannot build scores every mutant as
# a kill.
#
# THE SET IS DERIVED, never listed. A hand-kept list of embedding directories
# went stale the moment a new embed landed, and every mutant of every entry
# then failed to compile while the gate reported a perfect score.
#
# The listing runs in the REAL checkout, found through the laboratory's go.mod
# symlink: inside the laboratory `go list` cannot expand an embed pattern for
# the very reason this script exists.
#
# Arguments are passed to `go list` ahead of the package pattern — the build
# tags the caller compiles with, since a file can be embedded only under a tag.
set -eu

lab=$(pwd -P)
real=$(dirname "$(readlink -f go.mod)")
if [ "$real" = "$lab" ]; then
  echo "materialize_embeds.sh: go.mod is not a symlink — cwd is not an ooze laboratory" >&2
  exit 1
fi

tmpl='{{$d := .Dir}}{{range .EmbedFiles}}{{$d}}/{{.}}
{{end}}{{range .TestEmbedFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestEmbedFiles}}{{$d}}/{{.}}
{{end}}'

# Captured before the loop, not piped into it: a failed listing must stop the
# script here, and in a pipeline `set -e` sees only the loop's status.
if ! files=$(go -C "$real" list -deps -test "$@" -f "$tmpl" ./...); then
  echo "materialize_embeds.sh: could not list the module's embedded files in $real" >&2
  exit 1
fi

printf '%s\n' "$files" | sort -u | while IFS= read -r f; do
  case "$f" in "$real"/*) ;; *) continue ;; esac
  rel=${f#"$real"/}
  # Not a symlink: already materialized, or the mutated file ooze wrote.
  [ -L "$rel" ] || continue
  rm -f "$rel"
  cp "$f" "$rel"
done
