# Shared build logic for ctxloom's three binaries (ctxloom, ltk, taskloom).
#
# Pulled in with just's `import` (NOT `mod`): its recipes and variables are
# shared into the importing module's namespace, so each cmd/<app>/justfile stays
# thin — the app's `build` calls _go-build / _compress passing ONLY what differs.
# These recipes run INSIDE the devcontainer: the app justfiles are composed into
# justfile.container via `mod` and reached through the root `_run`, where
# GOWORK=off is already in the environment.

# Repo root. Convention across the ctxloom family: local justfiles compute
#   TOP := `git rev-parse --show-toplevel`
# and use {{TOP}}-relative paths, because a module's recipes run with the working
# directory set to the module's own dir (cmd/<app>/), not the repo root. In a git
# WORKTREE mounted into the devcontainer the worktree's .git points at an
# unmounted host path, so git can't resolve the toplevel; fall back to just's
# justfile_directory() (the directory of the composed root justfile, i.e.
# /workspace inside the container) so the build still works. Fault tolerance over
# purity (see CLAUDE.md): a normal checkout uses git; the worktree-in-container
# edge case falls back without breaking the build.
_git_top := `git rev-parse --show-toplevel 2>/dev/null || true`
TOP := if _git_top == "" { justfile_directory() } else { _git_top }

# Version stamp — the SAME versionator invocation the root justfiles use, run
# identically in every app so all three binaries stamp the same value (lockstep).
# Standardized stamp format across the ctxloom family:
#   v<major.minor.patch>-<short-sha>-<YYYYMMDDTHHMMSS commit datetime, utc>
# versionator emits the compact datetime (no separator); sed inserts the 'T'.
version := `if v=$(versionator output version -t "{{Prefix}}{{MajorMinorPatch}}-{{ShortHash}}-{{BuildDateTimeCompact}}" --prefix 2>/dev/null); then v=$(echo "$v" | sed -E 's/([0-9]{8})([0-9]{6})$/\1T\2/'); d=$(versionator output version -t "{{Dirty}}" 2>/dev/null); if [ -n "$d" ]; then echo "$v-dirty"; else echo "$v"; fi; else echo dev; fi`

# Disable VCS stamping (git is often unusable inside the container worktree).
buildvcs := "-buildvcs=false"

# Trimpath for reproducible builds and smaller binaries.
trimpath := "-trimpath"

# Build one of the family's Go binaries with the standard flags. Only the
# differing bits are parameters:
#   PKG   - package to build  (e.g. {{TOP}}/cmd/ctxloom)
#   OUT   - output path       (e.g. {{TOP}}/ctxloom)
#   XPATH - ldflags -X symbol for the version (e.g. main.Version)
#   TAGS  - build tags, or "" for none
#   CGO   - "1" or "0"
#   EXTRA - extra `go build` flags, or "" (default). The ONE caller today is
#           the coverage build, which passes "-cover": an instrumented binary
#           has to come from this recipe rather than a parallel one, or the
#           thing being measured stops being the thing that ships.
# go build creates OUT's parent directory itself, so no mkdir is needed.
_go-build PKG OUT XPATH TAGS CGO EXTRA="":
    CGO_ENABLED={{CGO}} go build {{buildvcs}} {{trimpath}} {{ if TAGS == "" { "" } else { "-tags " + TAGS } }} {{EXTRA}} \
        -ldflags "-s -w -X {{XPATH}}={{version}}" \
        -o {{OUT}} {{PKG}}

# Compress a built binary in place with UPX (best, LZMA).
_compress OUT:
    upx --best --lzma {{OUT}}
