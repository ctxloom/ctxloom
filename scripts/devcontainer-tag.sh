#!/usr/bin/env bash
# Print the content-addressed tag for the devcontainer image: the first 12 hex
# of a sha256 over EVERYTHING that decides what the image contains --
# .devcontainer/tool-versions.env (the build args), .devcontainer/Dockerfile
# itself, and every build-context file the Dockerfile COPYs or ADDs.
#
# This is the ONE definition of that tag. The root justfile's devcontainer_tag
# calls it, and every recipe that names the image goes through that variable;
# internal/shared/buildpins asserts both.
#
# Why the Dockerfile is in the key, not only the pins: the tag is shared by
# every worktree and session on the machine. Keyed on the pins alone, a
# Dockerfile edit in one tree rebuilt the image UNDER THE SAME TAG -- silently
# replacing the toolchain every other tree was gating against -- and a tree
# with the edit but no rebuild ran its gates on a stale image. Anything that
# changes the image's bytes must change its name.
#
# The copied files are DERIVED from the Dockerfile rather than listed here, so
# adding a COPY cannot leave the key blind to it. `COPY --from=` reads another
# stage or image, whose definition is already in the hashed Dockerfile; remote
# ADD sources are URLs spelled in the Dockerfile too. A source that does not
# exist, or a form this parser does not understand (heredocs), FAILS rather
# than being skipped: a key that quietly omits an input is the bug this exists
# to prevent.
set -euo pipefail

root="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$root"

dockerfile=.devcontainer/Dockerfile
pins=.devcontainer/tool-versions.env

if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
	sha() { shasum -a 256 "$@"; }
else
	echo "devcontainer-tag: need sha256sum or shasum to key the image" >&2
	exit 1
fi

# One logical instruction per line: join backslash continuations, drop
# comments and blanks, then keep the COPY/ADD instructions.
instructions="$(awk '
	/^[[:space:]]*#/ { next }
	{
		line = $0
		if (sub(/\\[[:space:]]*$/, "", line)) { cont = cont line " "; next }
		print cont line
		cont = ""
	}
' "$dockerfile" | grep -iE '^[[:space:]]*(COPY|ADD)[[:space:]]' || true)"

sources=()
while IFS= read -r ins; do
	[ -n "$ins" ] || continue
	case "$ins" in
	*"<<"*)
		echo "devcontainer-tag: heredoc COPY/ADD is not understood, so its inputs cannot be keyed: $ins" >&2
		exit 1
		;;
	esac
	# JSON (exec) form: strip the brackets, quotes and commas into words.
	if [[ "$ins" =~ \[ ]]; then
		ins="$(printf '%s' "$ins" | tr -d '[]",')"
	fi
	read -r -a words <<<"$ins"
	args=()
	from_other=0
	for w in "${words[@]:1}"; do
		case "$w" in
		--from=*) from_other=1 ;;
		--*) ;;
		*) args+=("$w") ;;
		esac
	done
	[ "$from_other" -eq 0 ] || continue
	[ "${#args[@]}" -ge 2 ] || {
		echo "devcontainer-tag: cannot parse sources from: $ins" >&2
		exit 1
	}
	# The last word is the destination; the rest are sources.
	for src in "${args[@]:0:${#args[@]}-1}"; do
		case "$src" in
		http://* | https://* | git@*) continue ;;
		esac
		sources+=("$src")
	done
done <<<"$instructions"

files=()
for src in "${sources[@]+"${sources[@]}"}"; do
	matched=0
	# Unquoted on purpose: a COPY source may be a glob.
	for path in $src; do
		[ -e "$path" ] || continue
		matched=1
		while IFS= read -r f; do files+=("$f"); done < <(find "$path" -type f | LC_ALL=C sort)
	done
	if [ "$matched" -eq 0 ]; then
		echo "devcontainer-tag: $dockerfile copies '$src', which matches nothing in the build context" >&2
		exit 1
	fi
done

sha "$pins" "$dockerfile" "${files[@]+"${files[@]}"}" | sha | cut -c1-12
