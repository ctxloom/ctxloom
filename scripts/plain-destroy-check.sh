#!/usr/bin/env bash
# A plain build (no `session-destroy`) has no way to delete a session (owner
# rulings 94, 94b, 98): `session remove`, which destroys a session (its
# stream, its runs' streams, its directory and lock), and `session sweep`,
# whose apply is that destroy, are compiled only under the `session-destroy`
# feature, and `session purge` exists in no build. This runs the binary a
# plain `cargo build` made, in a temporary home, and refuses one where any of
# `session remove`, `session purge` or `session sweep` is anything but clap's
# unknown subcommand, or whose `session --help` names one.
#
# The unit tests cannot see this: a workspace test build unifies features.
# Only a build without the dev-dependencies (`just build`) makes the binary
# that ships.
#
# The API too (owner M5): the plain binary's daemon answers the `Ctxloom`
# service's DeleteSession and SweepSessions UNIMPLEMENTED (grpc-status 12)
# to the user bearer, while the service itself is mounted and gated first
# (owner, after step 5: GetDaemon and DeleteSession without the bearer are
# both UNAUTHENTICATED, 16). The probe is curl over HTTP/2 to the endpoint
# the daemon logs (`-k`: nothing is trusted here, only a status read back),
# presenting the bearer read from the daemon's discovery file (its field 5,
# a 64-hex string: the bytes `*@` then the hex; given to curl from a 0600
# file, never on its command line); skipped, and said, where curl cannot
# speak HTTP/2.
#
# Usage: plain-destroy-check.sh <ctxloom binary>
# Silent on a pass; on a failure the first line is the summary, then what the
# binary said; exit 1.
set -euo pipefail
bin=${1:?usage: plain-destroy-check.sh <ctxloom binary>}
# Windows names the binary <name>.exe (cargo's target/debug/ctxloom.exe).
[ -f "$bin.exe" ] && bin=$bin.exe
bin=$(realpath "$bin")
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
# On Windows (SYSTEMROOT set; never on unix, where nothing is added) the
# binary's home is USERPROFILE and its temporary directory TEMP or TMP, in
# Windows' own form, and a Windows program needs SYSTEMROOT and WINDIR.
windows=()
root=${SYSTEMROOT:-${SystemRoot:-}}
if [ -n "$root" ]; then
    native=$(cygpath -w "$home")
    windows=(USERPROFILE="$native" TEMP="$native" TMP="$native" SYSTEMROOT="$root" WINDIR="$root")
fi
ctxloom() { (cd "$home" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" "$@"); }

help=$(ctxloom session --help 2>&1) || true
for verb in remove purge sweep; do
    rc=0
    said=$(ctxloom session "$verb" amber-swift-owl --yes --format text 2>&1) || rc=$?
    if [ "$rc" -ne 20 ] || ! grep -qi "unrecognized subcommand\|unknown verb\|did you mean\|unexpected argument" <<< "$said"; then
        echo "plain build: session $verb is not an unknown verb (exit $rc)"
        echo "$said"
        exit 1
    fi
    if grep -qE "^\s+$verb\b" <<< "$help"; then
        echo "plain build: session --help names $verb"
        echo "$help"
        exit 1
    fi
done

# The API probe (M5).
if ! curl --version 2>/dev/null | grep -qi 'http2'; then
    echo "plain-destroy-check: curl cannot speak HTTP/2; the API probe is skipped" >&2
    exit 0
fi
# One daemon per OS USER: the plain daemon's single-instance lock and its
# discovery file are the ACCOUNT's (`<account home>/.ctxloom/daemon/`), and a
# plain build has no override, so the probe's daemon takes the account's real
# lock and writes its real discovery file. Only in CI (`CI` set, as GitHub's
# runners set it), where the account is the runner's; elsewhere skipped, and
# said, so a build on her machine never touches her account's daemon.
if [ -z "${CI:-}" ]; then
    echo "plain-destroy-check: the API probe runs in CI alone (it takes the account's daemon lock); skipped" >&2
    exit 0
fi
if [ -n "$root" ]; then
    account=$(cygpath -u "${USERPROFILE:?}")
else
    account=$(bash -c "echo ~$(id -un)")
fi
# Its ctxloom home is the temporary one; it serves that, under the
# account's lock.
(cd "$home" && env -i PATH="$PATH" HOME="$home" CTXLOOM_HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" daemon serve --idle-seconds 30 >/dev/null 2>&1) &
daemon=$!
trap 'kill "$daemon" 2>/dev/null || true; rm -rf "$home"' EXIT
log="$home/.ctxloom/daemon/daemon.log"
endpoint=""
for _ in $(seq 1 200); do
    endpoint=$( { sed -n 's/.*serving at \(https:[^ ]*\).*/\1/p' "$log" 2>/dev/null || true; } | tail -1)

    [ -n "$endpoint" ] && break
    sleep 0.05
done
if [ -z "$endpoint" ]; then
    echo "plain build: its daemon never served"
    cat "$log" 2>/dev/null || true
    exit 1
fi
discovery="$account/.ctxloom/daemon/daemon.discovery"
user_bearer=$( { LC_ALL=C grep -ao '\*@[0-9a-f]\{64\}' "$discovery" 2>/dev/null || true; } | head -1)
user_bearer=${user_bearer#\*@}
if [ "${#user_bearer}" -ne 64 ]; then
    echo "plain build: its daemon's discovery file holds no user bearer"
    exit 1
fi
headers="$home/bearer.header"
(umask 077 && printf 'authorization: Bearer %s\n' "$user_bearer" > "$headers")
unset user_bearer
frame="$home/empty.grpc"
printf '\0\0\0\0\0' > "$frame"
# status <method> [<header file>]: the grpc-status the daemon answers.
status() {
    local auth=()
    [ -n "${2:-}" ] && auth=(-H "@$2")
    curl -sS -k --http2 --max-time 5 -o /dev/null -D - \
        -H 'content-type: application/grpc' -H 'te: trailers' \
        ${auth[@]+"${auth[@]}"} \
        --data-binary "@$frame" "$endpoint/ctxloom.v1.Ctxloom/$1" 2>&1 |
        tr -d '\r' | sed -n 's/^grpc-status: *//Ip' | head -1 || true
}
for method in GetDaemon DeleteSession; do
    got=$(status "$method")
    if [ "$got" != 16 ]; then
        echo "plain build: its daemon's $method without a bearer is grpc-status '$got', not 16 (UNAUTHENTICATED)"
        exit 1
    fi
done
for method in DeleteSession SweepSessions; do
    got=$(status "$method" "$headers")
    if [ "$got" != 12 ]; then
        echo "plain build: its daemon answers $method to the user bearer with grpc-status '$got', not 12 (UNIMPLEMENTED)"
        exit 1
    fi
done
(cd "$home" && env -i PATH="$PATH" HOME="$home" CTXLOOM_HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" daemon stop >/dev/null 2>&1) || true
wait "$daemon" 2>/dev/null || true
