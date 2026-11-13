# The pre-commit gates and their recipes (fmt, fmt-check, test, lint-deps; the
# private hook entry points fmt-staged, test-staged, test-commit,
# mutants-staged, secrets-scan-staged, push-check, merge-check; and the `_run`
# machinery and extractors) are gates/gates.just, imported below; this file
# gives them ctxloom's parameters and keeps its own recipes (`build`,
# `lint`, `test-pkg`, `clean-target`, `mutants-batch`, `closeout`,
# `selftest`). `just --list` shows the public ones by group; the private ones
# still run by name (`just test-staged`).
# cargo-deny's duplicate-version warnings stay in target/logs/lint-deps.log,
# not silenced in deny.toml.

TOP := `git rev-parse --show-toplevel`

import 'gates/gates.just'
import 'gates/os-gates.just'
import 'gates/fixture-gates.just'
import 'gates/arch-gates.just'
import 'gates/dev-local.just'

# What gates/arch-gates.just takes from this justfile: the arch-lint binary, built
# from the same ctxloom-lib commit the workspace pins (arch_lint_rev, kept
# equal to Cargo.toml's by `_arch-lint-ready`'s check) and installed once per
# commit into the user's cache, shared by every worktree.
arch_lint_rev := "fb8e821250b3c1935a25323a830de04bf2f844f9"
arch_lint_root := env_var_or_default("XDG_CACHE_HOME", env_var("HOME") / ".cache") / "ctxloom-arch-lint" / arch_lint_rev
arch_lint := arch_lint_root / "bin/arch-lint"

# The staged tree is a sibling of the checkout, not under target/: cargo
# reads .cargo/config.toml from every parent directory, so under the checkout
# it would pick up a local-deps override (.cargo/local-deps.toml) and gate the
# sibling checkouts instead of the commit's pinned dependencies.
staged_tree := "../.{name}.staged"

# No feature is turned on by what a commit changes, and the mutation run
# refuses any (owner ruling 99: `_mutants-no-features`). session-cli's tests
# turn on its `session-destroy` themselves, by a dev-dependency on the crate.
feature_rules := ""
features_prepare := ""

# No judge of a red commit (owner ruling of 2026-11-13, fast hooks): a red
# commit off the default branch goes through, and the landing gate's ratchet
# (`land-ratchet`, `closeout-ratchet`) refuses red scenarios the branch added
# or green ones it broke.
red_judge := ""

# Mutation kill-rate thresholds, percent, by crate path: the one place they
# are stated. A mutant is judged by the longest prefix its file matches; one
# matching none fails the gate, so a new tier gets a threshold before it gets
# code. core/wire is not here: .cargo/mutants.toml excludes it (generated).
# root/bin at 0 reports its survivors and refuses nothing: the composition root
# holds no logic, and the binary's wiring is shown by running it, not by unit
# tests.
mutation_thresholds := "crates/core/domain=90 crates/core/read-model=90 crates/core/ports=90 crates/core/application=90 crates/adapters=90 crates/root/lib=0 crates/root/bin=0"

# Staged paths outside any crate that can change what every crate builds or
# tests (bash globs, `*` crossing `/`): one of these staged tests the whole
# workspace.
workspace_wide := "Cargo.toml Cargo.lock rust-toolchain.toml proto/* .cargo/config.toml .cargo/config .config/nextest.toml scripts/nextest-build-binaries.sh"

# The cargo arguments `check-windows` (gates/os-gates.just) compiles for a
# Windows target: driven adapters that use the platform seams, without C
# dependencies (those need a mingw C compiler).
windows_packages := "-p content-fs -p config-files -p cache-files -p surface-files -p remote-git -p liveness -p session-files -p deps-files -p editor-process"

# Where this machine's checkout of ctxloom-lib's templates/clippy-disallowed.toml
# is, the canonical OS and filesystem list `lint-clippy-sync` compares
# clippy.toml with. A sibling checkout of the repository by default; set
# CTXLOOM_LIB_TEMPLATE to the file's path elsewhere (a worktree).
clippy_template := env("CTXLOOM_LIB_TEMPLATE", TOP + "/../ctxloom-lib/templates/clippy-disallowed.toml")

# Build every crate. Needs protoc on PATH and access to github.com (angzarr-proto).
[group('check')]
[doc("Build every crate (needs protoc and github.com)")]
[no-exit-message]
build:
    #!/usr/bin/env bash
    set -euo pipefail
    terse=(-q --message-format=short)
    [ "${VERBOSE:-}" = 1 ] && terse=()
    # Then the plain binary it made is held to knowing only the real engines
    # (ruling 52: the mocks are test-support's; scripts/plain-engines-check.sh)
    # and to having no destroy (ruling 94b: `session purge` is the
    # session-destroy feature's; scripts/plain-destroy-check.sh).
    target=${CARGO_TARGET_DIR:-{{TOP}}/target}
    exec just --justfile {{TOP}}/justfile _run build workspace cargo \
        bash -c 'cargo build "${@:3}" --manifest-path "$1/Cargo.toml" --workspace &&
            "$1/scripts/plain-engines-check.sh" "$2/debug/ctxloom" &&
            "$1/scripts/plain-destroy-check.sh" "$2/debug/ctxloom"' build {{TOP}} "$target" "${terse[@]}"

# Install the Rust ctxloom beside the Go one (owner ruling 164): the PLAIN
# binary (no test-support, no session-destroy; held to that by the same two
# checks `just build` runs), in the profile `just build` uses (the workspace
# defines no release profile of its own), as
# ~/.local/lib/ctxloom/ctxloom, and the wrapper ~/.local/bin/ctxloom (ahead of
# a Go ctxloom in ~/go/bin on PATH) that runs it with its own home,
# ~/.ctxloom-rs, so Go's ~/.ctxloom is never read. Its hooks call `ctxloom`,
# the running binary's file name (ruling 163), which leads the engine's PATH.
# Idempotent: a file already as it should be is left alone and said so. One
# line per action.
[group('install')]
[doc("Build the plain binary and install it as ~/.local/bin/ctxloom (home ~/.ctxloom-rs)")]
[no-exit-message]
install:
    #!/usr/bin/env bash
    set -euo pipefail
    terse=(-q --message-format=short)
    [ "${VERBOSE:-}" = 1 ] && terse=()
    target=${CARGO_TARGET_DIR:-{{TOP}}/target}
    built="$target/debug/ctxloom"
    just --justfile {{TOP}}/justfile _run install-build ctxloom cargo \
        bash -c 'cargo build "${@:4}" --manifest-path "$1/Cargo.toml" --package ctxloom --bin ctxloom &&
            "$1/scripts/plain-engines-check.sh" "$2" &&
            "$1/scripts/plain-destroy-check.sh" "$2"' install {{TOP}} "$built" "${terse[@]}"
    lib="$HOME/.local/lib/ctxloom"
    program="$lib/ctxloom"
    wrapper="$HOME/.local/bin/ctxloom"
    mkdir -p "$lib" "$HOME/.local/bin"
    if cmp -s "$built" "$program"; then
        echo "ok   unchanged $program"
    else
        # Beside it, then renamed over it: a running ctxloom keeps its file.
        install -m 0755 "$built" "$program.new"
        mv -f "$program.new" "$program"
        echo "ok   installed $program"
    fi
    text='#!/bin/sh
    export CTXLOOM_HOME="$HOME/.ctxloom-rs"
    exec "$HOME/.local/lib/ctxloom/ctxloom" "$@"'
    text=$(sed 's/^    //' <<< "$text")
    if [ -f "$wrapper" ] && [ "$(cat "$wrapper")" = "$text" ] && [ "$(stat -c %a "$wrapper")" = 755 ]; then
        echo "ok   unchanged $wrapper"
    else
        printf '%s\n' "$text" > "$wrapper.new"
        chmod 0755 "$wrapper.new"
        mv -f "$wrapper.new" "$wrapper"
        echo "ok   wrote $wrapper"
        # A recipe is a child process: it cannot clear the calling shell's
        # command hash, which may still map ctxloom to Go's binary.
        echo "note run 'rehash' (zsh) or 'hash -r' (bash) in shells that ran ctxloom before"
    fi

# Install the Rust ctxloom on Windows (owner ruling: the exe on PATH and a
# user CTXLOOM_HOME), under Git Bash: the PLAIN binary (`cargo build` of the
# package, no feature named), as %LOCALAPPDATA%\Programs\ctxloom\ctxloom.exe,
# and the USER variable CTXLOOM_HOME set to %USERPROFILE%\.ctxloom-rs with
# `setx`, only when the user's own value (HKCU\Environment) is not that
# already. There is no wrapper: the home is the variable, which every new
# terminal and every engine's hook process (ruling 112's CTXLOOM_HOME) sees.
# The directory is put on nobody's PATH: when the current PATH lacks it, the
# recipe says how to add it to the user's Path, and edits no registry key for
# it. A running ctxloom.exe cannot be overwritten but can be renamed, so the
# old one is moved aside first. Idempotent, one line per action, like
# `install`. The two plain-build checks `install` runs are not run here: they
# run the binary with `env -i`, which on Windows leaves it no SYSTEMROOT and
# no USERPROFILE (`just install` and `just build` hold the same build to them
# on unix). Unverified: written without a Windows machine to run it on.
[group('install')]
[doc("On Windows (Git Bash): install ctxloom.exe in %LOCALAPPDATA%\\Programs\\ctxloom, CTXLOOM_HOME a user variable")]
[no-exit-message]
install-windows:
    #!/usr/bin/env bash
    set -euo pipefail
    case "$(uname -s)" in
        MINGW*|MSYS*|CYGWIN*) ;;
        *) echo "FAIL install-windows: run it under Git Bash on Windows; elsewhere it is \`just install\`" >&2; exit 1 ;;
    esac
    : "${LOCALAPPDATA:?FAIL install-windows: LOCALAPPDATA is not set}"
    : "${USERPROFILE:?FAIL install-windows: USERPROFILE is not set}"
    terse=(-q --message-format=short)
    [ "${VERBOSE:-}" = 1 ] && terse=()
    target=${CARGO_TARGET_DIR:-{{TOP}}/target}
    built="$target/debug/ctxloom.exe"
    just --justfile {{TOP}}/justfile _run install-build ctxloom cargo \
        bash -c 'cargo build "${@:2}" --manifest-path "$1/Cargo.toml" --package ctxloom --bin ctxloom' \
        install {{TOP}} "${terse[@]}"
    win_dir="$LOCALAPPDATA\\Programs\\ctxloom"
    dir=$(cygpath -u "$win_dir")
    program="$dir/ctxloom.exe"
    mkdir -p "$dir"
    if cmp -s "$built" "$program"; then
        echo "ok   unchanged $program"
    else
        rm -f "$program.old" 2>/dev/null || true
        [ -e "$program" ] && mv -f "$program" "$program.old"
        cp "$built" "$program.new"
        mv -f "$program.new" "$program"
        rm -f "$program.old" 2>/dev/null || true
        echo "ok   installed $program"
    fi
    # The user's own variables, as the registry holds them (not this shell's,
    # which an enclosing session may have set). MSYS2_ARG_CONV_EXCL keeps Git
    # Bash from reading `/v` as a path.
    user_var() {
        MSYS2_ARG_CONV_EXCL='*' reg query 'HKCU\Environment' /v "$1" 2>/dev/null | tr -d '\r' |
            awk -v name="$1" 'toupper($1) == toupper(name) { $1 = ""; $2 = ""; sub(/^ +/, ""); print }'
    }
    home="$USERPROFILE\\.ctxloom-rs"
    if [ "$(user_var CTXLOOM_HOME)" = "$home" ]; then
        echo "ok   unchanged CTXLOOM_HOME=$home (user)"
    else
        MSYS2_ARG_CONV_EXCL='*' setx CTXLOOM_HOME "$home" >/dev/null
        echo "ok   set CTXLOOM_HOME=$home (user; a new terminal sees it)"
    fi
    case ":$PATH:" in
        *":$dir:"*) echo "ok   on PATH $win_dir" ;;
        *) echo "todo add $win_dir to your user Path (Settings > System > About > Advanced system settings > Environment Variables), then open a new terminal" ;;
    esac

# Build the binary that ships (owner ruling 2026-11-12): the PLAIN binary (no
# test-support, no session-destroy; held to that by the two checks `just
# build` runs), versionator-stamped by build.rs, in `profile`: `release` (the
# default; Cargo.toml's release profile, fat LTO with line tables, which
# `release-package` splits and strips) or `ci-artifact` (CI's per-push
# artifact on main: cargo's release defaults, no LTO, unstripped). The gate's
# `ok`/`FAIL` line goes to stderr; stdout is the binary's path alone
# (`ctxloom.exe` on Windows), for a caller to take with `$(just release-binary)`.
# CARGO_BUILD_TARGET, when set, is cargo's target (a cross build: ctxloom/ci's
# binaries workflow builds x86_64-apple-darwin on an arm64 macOS runner), and
# the binary is under target/<triple>/<profile>/.
[group('install')]
[doc("Build the plain binary (profile release|ci-artifact) and print its path")]
[no-exit-message]
release-binary profile="release":
    #!/usr/bin/env bash
    set -euo pipefail
    case '{{profile}}' in
        release | ci-artifact) ;;
        *) echo "FAIL release-binary: profile '{{profile}}' is not release or ci-artifact" >&2; exit 1 ;;
    esac
    terse=(-q --message-format=short)
    [ "${VERBOSE:-}" = 1 ] && terse=()
    target=${CARGO_TARGET_DIR:-{{TOP}}/target}
    built="$target/${CARGO_BUILD_TARGET:+$CARGO_BUILD_TARGET/}{{profile}}/ctxloom"
    just --justfile {{TOP}}/justfile _run release-binary ctxloom cargo \
        bash -c 'cargo build "${@:5}" --profile "$3" --manifest-path "$1/Cargo.toml" --package ctxloom --bin ctxloom &&
            "$1/scripts/plain-engines-check.sh" "$2" &&
            "$1/scripts/plain-destroy-check.sh" "$2"' release-binary {{TOP}} "$built" '{{profile}}' "${terse[@]}" >&2
    [ -f "$built.exe" ] && built=$built.exe
    echo "$built"

# The release's files for `os` (linux, macos or windows): the release-profile
# binary, stripped, next to its debug file (linux `<name>.debug` by its
# .gnu_debuglink and build id, macOS `<name>.dSYM.zip`, Windows `<name>.exe`
# with `<name>.pdb`), in `out`, named `<name>` (release.yml's
# `ctxloom-<stamp>-<os>-<arch>`; by default `ctxloom-<stamp>-<os>-<machine>`).
# scripts/release-package.sh does the work; on linux it also proves the debug
# file resolves an address of the stripped binary to file:line when an
# llvm-symbolizer is found. stdout: the files' paths, one per line.
# `profile` is release-binary's: `ci-artifact` (debug = 0, nothing to split)
# packages the binary alone, copied as `<name>[.exe]`, one line.
[group('install')]
[doc("Package the release binary with its debug file: release-package <linux|macos|windows> [out] [name] [profile]")]
[no-exit-message]
release-package os out="dist" name="" profile="release":
    #!/usr/bin/env bash
    set -euo pipefail
    bin=$(just --justfile {{TOP}}/justfile release-binary '{{profile}}')
    name='{{name}}'
    if [ -z "$name" ]; then
        stamp=$("$bin" version --format json | jq -r .version | tr -d '\r')
        name="ctxloom-$stamp-{{os}}-$(uname -m)"
    fi
    if [ '{{profile}}' = ci-artifact ]; then
        mkdir -p '{{out}}'
        ext=; [ "${bin%.exe}" != "$bin" ] && ext=.exe
        cp "$bin" "{{out}}/$name$ext"
        echo "$(cd '{{out}}' && pwd)/$name$ext"
        exit 0
    fi
    {{TOP}}/scripts/release-package.sh '{{os}}' "${bin%.exe}" '{{out}}' "$name"

# Install the arch-lint binary at the pinned ctxloom-lib commit, unless it is
# there; and refuse a pin that differs from the one Cargo.toml has.
[private]
_arch-lint-ready:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! grep -q 'ctxloom-lib", rev = "{{arch_lint_rev}}"' {{TOP}}/Cargo.toml; then
        echo "FAIL lint-arch: arch_lint_rev in the justfile is not the ctxloom-lib rev Cargo.toml pins" >&2
        exit 1
    fi
    [ -x '{{arch_lint}}' ] && exit 0
    cargo install --quiet --git https://github.com/ctxloom/ctxloom-lib --rev {{arch_lint_rev}} \
        arch-lint --locked --root '{{arch_lint_root}}' 2>&1 | tail -n 20

# Clippy, warnings denied, including the core's purity rules
# (crates/core/clippy.toml), after `lint-os` and `lint-arch`: lefthook's `clippy` step. Clippy
# reads only the nearest clippy.toml, so the core's repeats the root's nesting
# threshold and every method the root's disallows (the file I/O the
# file-system seam holds); refuse to lint if they differ.
[group('check')]
[doc("Clippy, warnings denied, with the core's purity rules and the OS-cfg check")]
[no-exit-message]
lint: lint-arch lint-path-deps
    #!/usr/bin/env bash
    set -euo pipefail
    root=$(grep '^excessive-nesting-threshold' {{TOP}}/clippy.toml)
    core=$(grep '^excessive-nesting-threshold' {{TOP}}/crates/core/clippy.toml)
    if [ "$root" != "$core" ]; then
        echo "FAIL lint: clippy.toml ($root) and crates/core/clippy.toml ($core) disagree on nesting" >&2
        exit 1
    fi
    paths() { grep -o 'path = "[^"]*"' "$1" | sort -u; }
    missing=$(comm -23 <(paths {{TOP}}/clippy.toml) <(paths {{TOP}}/crates/core/clippy.toml) | tr '\n' ' ')
    if [ -n "$missing" ]; then
        echo "FAIL lint: crates/core/clippy.toml does not disallow what clippy.toml does: $missing" >&2
        exit 1
    fi
    {{TOP}}/scripts/os-cfg-check.sh
    {{TOP}}/scripts/exec-mode-check.sh
    terse=(-q --message-format=short)
    [ "${VERBOSE:-}" = 1 ] && terse=()
    exec just --justfile {{TOP}}/justfile _run lint workspace cargo \
        cargo clippy "${terse[@]}" --manifest-path {{TOP}}/Cargo.toml --workspace --all-targets -- -D warnings

# No dependency, [patch] or [replace] entry reaches outside the repository by
# path, and no .cargo/config.toml is tracked (scripts/path-deps-check.sh): it
# judges the staged index. Local sibling checkouts go in the gitignored
# .cargo/config.toml. Lefthook's pre-commit `path-deps` runs this.
[group('check')]
[doc("No dependency path reaches outside the repository")]
[no-exit-message]
lint-path-deps:
    @just --justfile {{TOP}}/justfile _run path-deps staged path-deps {{TOP}}/scripts/path-deps-check.sh {{TOP}}

# Wire lefthook's git hooks into the repository's .git/hooks. Worktrees share
# .git/hooks, so this is once per clone, and again whenever lefthook.yml or
# gates/lefthook.yml names a hook not installed yet. Needs lefthook (1.13.6)
# and gitleaks (v8), both by `go install`; cargo-nextest, cargo-deny and
# cargo-mutants by `cargo install <name> --locked`; jq, flock, setsid, ps
# and pkill.
[group('setup')]
[doc("Install the git hooks (once per clone)")]
hooks-install:
    lefthook install

# The tests of the named crates only, by cargo-nextest (the gates' `_tests`):
# the quick loop on one crate. `just test` runs every crate's.
[group('check')]
[doc("Tests of the named crates only (nextest)")]
[no-exit-message]
test-pkg +packages: (_require-nextest "test-pkg")
    @just --justfile '{{TOP}}/justfile' _run test-pkg "{{packages}}" tests just --justfile '{{TOP}}/justfile' _tests {{packages}}

# Mutation-test a batch (base..head; base defaults to the merge-base with main,
# head to HEAD) in a throwaway worktree: scripts/mutants-batch.sh. Nothing else
# runs cargo-mutants but the `mutants-staged` gate recipe it drives, and the
# nightly's whole-workspace scoring (`mutants-shard`, `mutants-score`).
[group('closeout')]
[doc("Mutation-test a batch: mutants-batch [base] [head]")]
[no-exit-message]
mutants-batch base="" head="HEAD":
    @{{TOP}}/scripts/mutants-batch.sh '{{base}}' '{{head}}'

# One shard of a mutation run over the WHOLE workspace (owner ruling
# 2026-11-12: the nightly's full mutation run, scored by tier): cargo-mutants'
# `--shard k/n` (k from 0; its default slice sharding, consecutive mutants), in place (CI's checkout is
# throwaway), no feature (`_mutants-no-features`), .cargo/mutants.toml as
# ever (each mutant judged by its own crate's tests, under nextest). Its
# mutants.out and its cargo-mutants exit (`exit`) go to `out`. Exit 0 when
# cargo mutants judged (its 0, 2 missed or 3 timed out); its own code
# otherwise (4: the tests fail unmutated). `mutants-score` judges the shards
# together.
[group('closeout')]
[doc("One shard of a whole-workspace mutation run: mutants-shard <k/n> [out]")]
[no-exit-message]
mutants-shard shard out="target/logs/mutants-shard": _mutants-no-features
    #!/usr/bin/env bash
    set -euo pipefail
    out='{{out}}'
    case "$out" in /*) ;; *) out={{TOP}}/$out ;; esac
    rm -rf "$out"
    mkdir -p "$out"
    rc=0
    cargo mutants --dir {{TOP}} --in-place --workspace --shard '{{shard}}' --output "$out" --colors never || rc=$?
    echo "$rc" > "$out/exit"
    found=$(jq '.total_mutants // 0' "$out/mutants.out/outcomes.json" 2>/dev/null || echo 0)
    echo "mutants-shard {{shard}}: cargo mutants exit $rc, $found mutants"
    case "$rc" in 0 | 2 | 3) exit 0 ;; *) exit "$rc" ;; esac

# Score a whole-workspace mutation run from its shards' `mutants-shard`
# outputs: their outcomes.json merged (mutants found and outcomes summed),
# then judged by the gates' own verdict (`_mutants-verdict`, against
# `mutation_thresholds`), the worst shard's cargo-mutants exit as its rc.
# Writes `out`/verdict.log (the verdict's words) and `out`/scores.json (one
# object per tier: prefix, killed, viable, percent, threshold, pass; and the
# run's totals), and exits with the verdict's code.
[group('closeout')]
[doc("Score the shards of a whole-workspace mutation run: mutants-score <out> <shard dir>...")]
[no-exit-message]
mutants-score out +shards:
    #!/usr/bin/env bash
    set -euo pipefail
    out='{{out}}'
    rm -rf "$out"
    mkdir -p "$out/mutants.out"
    rc=0
    files=()
    for dir in {{shards}}; do
        [ -f "$dir/mutants.out/outcomes.json" ] || { echo "FAIL mutants-score: $dir holds no mutants.out/outcomes.json" >&2; exit 1; }
        files+=("$dir/mutants.out/outcomes.json")
        r=$(cat "$dir/exit")
        case "$r" in
            0 | 2 | 3) [ "$rc" -gt 3 ] || [ "$r" -le "$rc" ] || rc=$r ;;
            *) rc=$r ;;
        esac
    done
    jq -s '{total_mutants: (map(.total_mutants // 0) | add), outcomes: (map(.outcomes) | add)}' "${files[@]}" \
        > "$out/mutants.out/outcomes.json"
    vrc=0
    just --justfile {{TOP}}/justfile _mutants-verdict "$out" "$rc" > "$out/verdict.log" 2>&1 || vrc=$?
    cat "$out/verdict.log"
    # The verdict's tier lines, its first block only (a failing verdict says
    # them again): `  pass  crates/x: 12/13 killed (92%), threshold 90%...` or
    # `  pass  crates/x: none judged...`.
    found=$(jq '.total_mutants' "$out/mutants.out/outcomes.json")
    awk '/kill rate by tier:$/ { if (seen++) exit; next } seen && /^ +(pass|FAIL) / { print }' "$out/verdict.log" |
        sed -nE -e 's/^ +(pass|FAIL) +([^:]+): ([0-9]+)\/([0-9]+) killed \(([0-9]+)%\), threshold ([0-9]+)%.*$/\1 \2 \3 \4 \5 \6/p' \
            -e 's/^ +(pass|FAIL) +([^:]+): none judged.*$/\1 \2 0 0 -1 -1/p' |
        jq -R -s --argjson found "$found" --argjson shards "${#files[@]}" --argjson rc "$vrc" '
            split("\n") | map(select(. != "") | split(" ")
                | {prefix: .[1], killed: (.[2] | tonumber), viable: (.[3] | tonumber),
                   percent: (.[4] | tonumber | if . < 0 then null else . end),
                   threshold: (.[5] | tonumber | if . < 0 then null else . end), pass: (.[0] == "pass")})
            | {mutants: $found, shards: $shards, verdict_exit: $rc, tiers: .}' > "$out/scores.json"
    exit "$vrc"

# The whole mechanical close-out: build, lint, lint-deps, test, lint-os and
# mutants-batch, one summary line each (scripts/closeout.sh).
[group('closeout')]
[doc("Close-out gates in one run: closeout [base]")]
[no-exit-message]
closeout base="":
    @{{TOP}}/scripts/closeout.sh '{{base}}'

# The close-out of a slice landing on an integration branch that carries a
# red spec (owner ruling 165): closeout's gates, but the feature runner may
# fail where the base's does, and nowhere else (scripts/closeout-ratchet.sh;
# the verdict is scripts/ratchet-judge.sh).
[group('closeout')]
[doc("Close-out on an integration branch with a red spec: closeout-ratchet <base>")]
[no-exit-message]
closeout-ratchet base:
    @{{TOP}}/scripts/closeout-ratchet.sh '{{base}}'

# The landing gate of a branch on an integration branch (owner ruling of
# 2026-11-13): closeout-ratchet's gates and ratchet without mutation
# (scripts/closeout-ratchet.sh --no-mutation). Each branch landing on the
# integration branch runs it; closeout-ratchet, mutation included, runs once
# per batch and before main.
[group('closeout')]
[doc("Landing gate on an integration branch, no mutation: land-ratchet <base>")]
[no-exit-message]
land-ratchet base:
    @{{TOP}}/scripts/closeout-ratchet.sh --no-mutation '{{base}}'

# The base's scenario list for the ratchet, cached by its sha
# (scripts/ratchet-base.sh): the feature runner on the base's tip in a
# throwaway worktree.
[group('closeout')]
[doc("The red and green scenarios of a base commit: ratchet-base <base>")]
[no-exit-message]
ratchet-base base:
    @{{TOP}}/scripts/ratchet-base.sh '{{base}}'

# Delete target/ (the only sanctioned `cargo clean`; ltk blocks the raw one).
[group('setup')]
[doc("Delete target/ (what `cargo clean` would)")]
clean-target:
    rm -rf {{TOP}}/target

# The self-tests of this repository's scripts against fixture trees.
[group('check')]
[doc("Self-tests of the scripts: drop-worktree, mutants-batch, closeout, closeout-ratchet, ltk rules, output budget, tests exit, test-staged, exec-mode check, path deps")]
[no-exit-message]
selftest:
    @{{TOP}}/scripts/drop-worktree.selftest.sh
    @{{TOP}}/scripts/mutants-batch.selftest.sh
    @{{TOP}}/scripts/mutants-verdict.selftest.sh
    @{{TOP}}/scripts/closeout.selftest.sh
    @{{TOP}}/scripts/closeout-ratchet.selftest.sh
    @{{TOP}}/scripts/ltk-rules-check.sh
    @{{TOP}}/scripts/output-budget.selftest.sh
    @{{TOP}}/scripts/tests-exit.selftest.sh
    @{{TOP}}/scripts/test-staged.selftest.sh
    @{{TOP}}/scripts/exec-mode-check.selftest.sh
    @{{TOP}}/scripts/dev-local.selftest.sh
    @{{TOP}}/scripts/path-deps-check.selftest.sh
    @{{TOP}}/scripts/commit-msg.selftest.sh
    @{{TOP}}/scripts/release-package.selftest.sh

# Run the ctxloom binary (cargo run, quiet) with the given arguments.
[group('dev')]
[doc("Run the ctxloom binary: run [args...]")]
run *args:
    @cargo run -q --manifest-path {{TOP}}/Cargo.toml --bin ctxloom -- {{args}}

# ===== Version (versionator, .versionator.yaml) =====
# VERSION holds the version; the binary's build script stamps it
# (crates/root/bin/ctxloom/build.rs): v<MAJOR.MINOR.PATCH[-pre]>-<short hash>,
# and on a tree with a tracked change -<YYYYMMDDTHHMMSS UTC>-dirty after it.
# A change of version also writes the binary's Cargo.toml (versionator's
# `updates:`) and records it in Cargo.lock. No release recipe: a human cuts
# releases.

# Show the version VERSION holds.
[group('version')]
[doc("Show the version VERSION holds")]
show-version:
    @cd {{TOP}} && versionator output version

# Set the version, e.g. `just set-version 0.2.0` or `0.2.0-rc.1`.
[group('version')]
[doc("Set the version: set-version <version>")]
set-version version:
    @cd {{TOP}} && versionator set {{ version }} && just --justfile {{TOP}}/justfile _version-lock

# Bump the patch version (0.0.X).
[group('version')]
[doc("Bump the patch version (0.0.X)")]
bump-patch:
    @cd {{TOP}} && versionator bump patch inc && just --justfile {{TOP}}/justfile _version-lock

# Bump the minor version (0.X.0).
[group('version')]
[doc("Bump the minor version (0.X.0)")]
bump-minor:
    @cd {{TOP}} && versionator bump minor inc && just --justfile {{TOP}}/justfile _version-lock

# Bump the major version (X.0.0).
[group('version')]
[doc("Bump the major version (X.0.0)")]
bump-major:
    @cd {{TOP}} && versionator bump major inc && just --justfile {{TOP}}/justfile _version-lock

# Bump by the conventional commits since the last tag (feat: minor, fix: patch).
[group('version')]
[doc("Bump by the conventional commits since the last tag")]
bump:
    @cd {{TOP}} && versionator bump && just --justfile {{TOP}}/justfile _version-lock

# Cargo.lock records the binary's new version (versionator never touches it).
_version-lock:
    @cargo update --workspace --offline --quiet --manifest-path {{TOP}}/Cargo.toml
