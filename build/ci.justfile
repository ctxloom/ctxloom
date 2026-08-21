# CI steps, as recipes. Imported by BOTH root justfiles.
#
# Every step in .github/workflows/ is `just <target>`; the shell that used to
# live in a `run:` block lives here instead. Two reasons, both learned the hard
# way in this repo:
#
#   1. A workflow's `run:` block is the one piece of build logic nobody can run
#      locally. It is only ever exercised on a runner, so it is only ever
#      debugged by pushing commits. Behind a recipe, `just version-untagged-check`
#      is a thing you type.
#   2. Shell that exists ONLY in a workflow has no counterpart to be checked
#      against, so it drifts silently from the recipe that does the same job.
#      That is exactly how CI came to run a bare `gremlins unleash`
#      (mutation-weekly.yml) while `test-mutation` right here pins TMPDIR to
#      disk to stop gremlins emptying a tmpfs — same operation, two
#      definitions, only one of them safe.
#
# Imported (like build/gates.justfile) rather than duplicated, so `justfile`
# and `justfile.container` share ONE definition. Nothing here may shell out to
# docker or assume the devcontainer: these recipes run on bare `ubuntu-latest`,
# inside the devcontainer image, and inside `goreleaser-cross`.
#
# GITHUB_* AWARENESS. A few recipes below write to $GITHUB_OUTPUT/$GITHUB_ENV/
# $GITHUB_PATH when those are set, and are plain stdout commands when they are
# not. That is deliberate: the alternative is a `run:` block that wraps the
# recipe in a heredoc, which puts shell back in the workflow and re-opens (1).

# Shared helper: an error that is a plain stderr message locally and ALSO a
# GitHub annotation (surfaced on the run summary, not just buried in the log)
# under Actions. Interpolated into the recipes that need it, so the annotation
# format lives in one place.
_gha_error := '''
gha_error() {
    printf '%s\n' "$*" >&2
    if [ -n "${GITHUB_ACTIONS:-}" ]; then printf '::error::%s\n' "$*"; fi
}
'''

# ===== CI environment =====

# Mark the checkout as a safe git directory.
#
# Needed by every job that runs git inside a container image: the checkout is
# owned by the runner's uid, the image's default user is a different one, and
# git refuses to operate on a repo it thinks belongs to someone else. Recipes
# like gen-docs-check (git diff), version-untagged-check (git rev-parse) and
# anything calling versionator fail with "detected dubious ownership" without
# this. No-op to run twice, and harmless outside CI.
ci-git-safe-directory:
    git config --global --add safe.directory "{{justfile_directory()}}"

# Put the repo root and bin/ on PATH for subsequent steps.
#
# The godog acceptance suite and tests/integration exec the REAL ctxloom binary
# as a subprocess and resolve it from PATH (or CTXLOOM_BINARY), and `build`
# writes ./ctxloom into the repo root. Under Actions this appends to
# $GITHUB_PATH, which is the only way one step can change a later step's PATH;
# run locally it just prints what it would add, since a recipe cannot mutate
# its caller's environment.
ci-path:
    #!/usr/bin/env bash
    set -euo pipefail
    root="{{justfile_directory()}}"
    if [ -n "${GITHUB_PATH:-}" ]; then
        printf '%s\n%s\n' "$root" "$root/bin" >> "$GITHUB_PATH"
    fi
    echo "PATH additions: $root, $root/bin"

# Identify commits made by CI as the Actions bot (auto-release.yml).
ci-git-config-bot:
    git config user.name "github-actions[bot]"
    git config user.email "github-actions[bot]@users.noreply.github.com"

# ===== Version / release gates =====

# Release gate: the VERSION in this tree must not already be tagged.
#
# Releases here are merge-triggered and the version is set DELIBERATELY with
# `versionator set` — there is no auto-increment. So a change reaching main
# carrying an already-released VERSION would either silently re-point an
# immutable tag or produce a release nobody asked for. Failing here forces the
# bump into the PR, where a human sees it.
version-untagged-check:
    #!/usr/bin/env bash
    set -euo pipefail
    {{_gha_error}}
    v="$(tr -d '[:space:]' < VERSION)"
    if [ -z "$v" ]; then
        gha_error "VERSION file is empty"
        exit 1
    fi
    tag="v${v#v}"
    if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
        gha_error "VERSION $v is already tagged ($tag). Bump it (versionator set <next>) before merging."
        exit 1
    fi
    echo "VERSION $v ($tag) is not yet tagged — OK to merge and release."

# Tag the VERSION in this tree and push the tag (auto-release.yml).
#
# Refuses an existing tag rather than skipping: a tag is immutable, so
# "already there" means the release pipeline has lost track of what it is
# building, and continuing would publish artifacts under a name that already
# means something else.
release-tag:
    #!/usr/bin/env bash
    set -euo pipefail
    {{_gha_error}}
    v="$(tr -d '[:space:]' < VERSION)"
    if [ -z "$v" ]; then
        gha_error "VERSION file is empty"
        exit 1
    fi
    tag="v${v#v}"
    if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
        gha_error "Tag $tag already exists. VERSION must be bumped per release; refusing to re-point an immutable tag."
        exit 1
    fi
    git tag "$tag"
    git push origin "$tag"
    echo "Tagged and pushed $tag — release-completer will build and publish the release."

# Build and publish the real release (release-completer.yml). Distinct from
# `release-check` (validates .goreleaser.yml) and `release-snapshot` (local,
# single-target, publishes nothing).
release-publish:
    goreleaser release --clean

# ===== Pinned tool versions =====
#
# .devcontainer/tool-versions.env is the single source of truth. Its consumers
# each need it in a different shape; the recipes below are those shapes. Keep
# deriving from the file — internal/buildpins' drift gate fails if a consumer
# starts hand-copying version numbers instead.

# Emit tool-versions.env as bare KEY=VALUE lines (comments and blanks stripped).
#
# Consumed by ci.yml's build-container job, which builds .devcontainer/Dockerfile
# through docker/build-push-action rather than `just dev-image`, and so has to
# pass the pins as that action's `build-args` input. Under Actions this also
# appends them as the multiline step output `args`; run locally it just prints.
tool-version-args:
    #!/usr/bin/env bash
    set -euo pipefail
    args="$(grep -v '^#' .devcontainer/tool-versions.env | grep -v '^$')"
    if [ -z "$args" ]; then
        echo "error: .devcontainer/tool-versions.env yielded no KEY=VALUE pairs" >&2
        exit 1
    fi
    printf '%s\n' "$args"
    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        {
            echo 'args<<TOOL_VERSIONS_EOF'
            printf '%s\n' "$args"
            echo 'TOOL_VERSIONS_EOF'
        } >> "$GITHUB_OUTPUT"
    fi

# Install the codegen/build tools release-completer.yml needs, at the versions
# .devcontainer/tool-versions.env pins.
#
# That job runs in goreleaser-cross, NOT the devcontainer image, so it cannot
# inherit the toolchain and must install its own copies. Sourcing the same file
# the Dockerfile reads is what stops the two drifting: buf.gen.yaml uses
# `local:` plugins, so protoc-gen-go/protoc-gen-go-grpc must be on PATH at the
# SAME versions CI generated against, and a release whose codegen silently used
# whatever happened to be installed is the failure this guards
# (internal/buildpins package doc).
release-install-tools:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a
    . .devcontainer/tool-versions.env
    set +a
    curl -sSL "https://github.com/benjaminabbitt/versionator/releases/download/v${VERSIONATOR_VERSION}/versionator-linux-amd64.tar.gz" -o /tmp/versionator.tar.gz
    tar -xzf /tmp/versionator.tar.gz -C /tmp
    install /tmp/versionator-linux-amd64 /usr/local/bin/versionator
    curl -sSL "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-Linux-x86_64" -o /usr/local/bin/buf
    chmod +x /usr/local/bin/buf
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION}
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}
    gobin="$(go env GOPATH)/bin"
    if [ -n "${GITHUB_PATH:-}" ]; then echo "$gobin" >> "$GITHUB_PATH"; fi
    echo "installed versionator ${VERSIONATOR_VERSION}, buf ${BUF_VERSION}, protoc-gen-go ${PROTOC_GEN_GO_VERSION}, protoc-gen-go-grpc ${PROTOC_GEN_GO_GRPC_VERSION} (plugins in $gobin)"

# SHA256 of the published install scripts, as KEY=VALUE.
#
# .goreleaser.yml interpolates these into the generated Homebrew cask/install
# instructions, so they must be computed from the exact bytes being released.
# Under Actions this also appends them to $GITHUB_ENV for the goreleaser step.
install-script-hashes:
    #!/usr/bin/env bash
    set -euo pipefail
    sh="$(sha256sum scripts/install.sh | cut -d' ' -f1)"
    ps1="$(sha256sum scripts/install.ps1 | cut -d' ' -f1)"
    printf 'HASH_INSTALL_SH=%s\nHASH_INSTALL_PS1=%s\n' "$sh" "$ps1"
    if [ -n "${GITHUB_ENV:-}" ]; then
        printf 'HASH_INSTALL_SH=%s\nHASH_INSTALL_PS1=%s\n' "$sh" "$ps1" >> "$GITHUB_ENV"
    fi

# ===== Mutation testing =====

# gremlins copies the whole Go module into TMPDIR once per worker. On a tmpfs
# /tmp that exhausts RAM and wedges the machine (it has emptied a 16G tmpfs
# here), so every recipe that runs gremlins pins TMPDIR to disk and sweeps the
# copies afterwards. Shared with the host justfile's other mutation recipes.
mutation_tmp := env_var_or_default("CTXLOOM_MUTATION_TMP", "/var/tmp/ctxloom-mutation")

# Run mutation tests with gremlins over the whole tree (requires gremlins
# installed). This is what the weekly cron runs; it is far too slow to gate a
# push.
# "$@" (not the ARGS interpolation) so a value containing shell metacharacters
# (e.g. a `|`-alternation regex) reaches gremlins intact instead of being
# re-parsed by this script's shell — see test-pkg for the failure mode.
test-mutation *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    TMPDIR="{{mutation_tmp}}" gremlins unleash "$@"

# Diff-only mutation testing against BASE — the per-push/per-PR gate.
#
# Runs on both PR and push so a green PR ran the same check that gates the
# release on main; the whole-codebase run is the weekly cron instead.
#
# Two skips, both of which would otherwise be spurious RED:
#   - no usable base (first push to a new branch, or the all-zeroes SHA GitHub
#     sends for a branch creation) — there is nothing to diff against;
#   - a diff with no mutable Go. gremlins exits 10 on an empty changeset
#     (0 mutants => 0% efficacy), so a docs/workflow/test-only change would
#     fail the gate for having nothing to test. The exclusion list mirrors
#     .gremlins.yaml's exclude-files.
test-mutation-diff BASE:
    #!/usr/bin/env bash
    set -euo pipefail
    base="$1"
    if [ -z "$base" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then
        echo "No base commit to diff against — skipping mutation testing."
        exit 0
    fi
    mutable="$(git diff --name-only "$base"...HEAD -- '*.go' \
        | grep -vE '(_test\.go$|\.pb\.go$|/mock_[^/]*\.go$|(^|/)testdata/|(^|/)tests/integration/)' || true)"
    if [ -z "$mutable" ]; then
        echo "No mutable Go files changed vs $base — skipping mutation testing."
        exit 0
    fi
    echo "Mutable Go files in diff:"
    printf '  %s\n' $mutable
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    TMPDIR="{{mutation_tmp}}" gremlins unleash --diff "$base"

# ===== Documentation site =====

# Substitute the release version into the docs source before the site build.
# Destructive to the working tree by design (the site build consumes the
# substituted files); CI runs it on a throwaway checkout.
docs-inject-version:
    #!/usr/bin/env bash
    set -euo pipefail
    version="$(tr -d 'v[:space:]' < VERSION)"
    if [ -z "$version" ]; then
        echo "error: VERSION file is empty — every {{{{VERSION}} placeholder would be replaced with nothing" >&2
        exit 1
    fi
    echo "Injecting version: $version"
    find website/src \( -name '*.md' -o -name '*.mdx' \) -print0 \
        | xargs -0 sed -i "s/{{{{VERSION}}/$version/g"

# Install the docs site's npm dependencies from the lockfile.
docs-deps:
    cd website && npm ci

# Build the docs site for production.
docs-build:
    cd website && npm run build

# ===== Engine drift detection =====

# Latest published version of ENGINE, from its public release feed.
# Under Actions this also sets the `version` step output.
engine-latest-version ENGINE:
    #!/usr/bin/env bash
    set -euo pipefail
    {{_gha_error}}
    engine="$1"
    version="$(.github/scripts/detect-engine-version.sh "$engine")"
    if [ -z "$version" ]; then
        gha_error "detect-engine-version.sh $engine produced no output"
        exit 1
    fi
    echo "Resolved $engine latest = $version"
    if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "version=$version" >> "$GITHUB_OUTPUT"; fi

# The tested-version lock for LOCK_KEY, from .github/engine-versions.env — the
# version ctxloom's transcript reader has actually been validated against.
# Under Actions this also sets the `version` step output.
engine-pinned-version LOCK_KEY:
    #!/usr/bin/env bash
    set -euo pipefail
    {{_gha_error}}
    key="$1"
    pinned="$(grep -E "^${key}=" .github/engine-versions.env | cut -d= -f2-)"
    if [ -z "$pinned" ]; then
        gha_error "$key not found in .github/engine-versions.env"
        exit 1
    fi
    echo "Pinned lock $key = $pinned"
    if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "version=$pinned" >> "$GITHUB_OUTPUT"; fi

# Open (or reuse) the tracking issue for an engine version ctxloom's reader
# has never been validated against. Requires `gh` authenticated via GH_TOKEN.
#
# Idempotent per new version: the labels are created --force (no-op if they
# exist) and an issue is only opened when one with this EXACT title is not
# already open — a daily cron would otherwise file a fresh duplicate every run
# until a human bumps the lock.
engine-drift-alert ENGINE PINNED LATEST RUN_URL:
    #!/usr/bin/env bash
    set -euo pipefail
    engine="$1"; pinned="$2"; latest="$3"; run_url="$4"
    title="engine-drift: $engine $pinned -> $latest"

    gh label create "engine-drift" --color "B60205" \
        --description "A new engine CLI version was detected that ctxloom's reader has not been validated against" \
        --force
    gh label create "engine-drift:$engine" --color "5319E7" \
        --description "Drift alert for the $engine engine specifically" \
        --force

    # Pipe to jq rather than gh's --jq: --arg is a JQ flag and gh does not forward
    # it, so gh rejects the command, prints its usage, and this recipe exits 1
    # before any alert is filed. Passing the title as a jq --arg (not string-
    # interpolating it) also keeps a title with quotes out of the jq program.
    existing="$(gh issue list --state open --label "engine-drift:$engine" --search "\"$title\" in:title" --json title | jq -r --arg t "$title" '.[] | select(.title == $t) | .title')"
    if [ -n "$existing" ]; then
        echo "An open issue already tracks $title; not creating a duplicate."
        exit 0
    fi

    body="$(cat <<EOF
    A new **$engine** CLI version was detected that is newer than the tested-version lock.

    | | |
    |---|---|
    | Pinned (\`.github/engine-versions.env\`) | \`$pinned\` |
    | Latest detected | \`$latest\` |
    | Detected by | \`.github/workflows/engine-drift-detect.yml\` ([run]($run_url)) |

    This is an **alert-only** notification (P0 of the self-healing engine-format
    pipeline) -- nothing has been installed, captured, or changed. A human should:

    1. Confirm \`internal/transcript/vendorreader/$engine\` (or the closest match --
       \`claude-code\` -> the \`claude\` reader package) still parses a transcript
       produced by \`$latest\`.
    2. If it does, bump \`$engine\`'s key in \`.github/engine-versions.env\` to
       \`$latest\` in a PR (internal/enginepins will fail CI if the bump isn't a
       valid semver or the workflow stops referencing the key by name).
    3. If it does not, file/track the parser fix separately -- this issue is only
       the drift signal, not the fix.

    (Later phases of the pipeline automate capture + the drift oracle + self-heal
    for the headless-capable engines; this repo does not yet run any of that.)
    EOF
    )"

    gh issue create --title "$title" --label "engine-drift" --label "engine-drift:$engine" --body "$body"
