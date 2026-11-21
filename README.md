# ctxloom

The Rust ctxloom, ported from the Go ctxloom (preserved on the `go-final` branch of ctxloom/ctxloom-legacy), piece by piece,
with architect review of each piece as it is ported.

## Notes on using it

These cover behaviour a command's `-h` does not spell out. Each was run
against a built binary in a fresh `HOME` and `CTXLOOM_HOME`, except where a
line says it is pinned by a test.

### The library needs no git

The library works in any directory, a git repository or not: `ctxloom init`
makes `.ctxloom/` where it is run, and nothing under the project or in
ctxloom's home is a git repository. Creating, editing and removing content
makes no commit, and ctxloom keeps no home repository. (git is used for
what is about remotes and scratch checkouts, not the library.)

### Looking again: `ctxloom context refresh`

The daemon remembers what it has seen of your library. `context refresh`
makes it forget that and read the library again now; use it after editing
files behind its back. In text it prints `the library was observed afresh:
what changed is recorded`; in json it prints `null`. It takes no argument.

### Removing a bundle: the preview

`bundle remove <bundle>` without `--yes` changes nothing and says what it
would take out, and which profiles and agents include the bundle, because a
run of each is refused until it is edited:

    Would remove bundle "demo":
      1 fragment(s), 1 command(s)
      profile dp includes it
      agent ag includes it
      a run of each is refused until it is edited

The json carries them as `dependents`. `--yes` applies it.

### The daemon

One daemon per user serves every command. `ctxloom daemon stop` asks it to
exit and says so, in text `stopped the ctxloom daemon (pid 1234), ctxloom
<version>`, or `no ctxloom daemon was serving: nothing to stop` when none
was; in json the daemon it stopped (`name`, `pid`, `endpoint`,
`start_time`, `version`, `idle_limit_seconds`), or `{}`. Both exit 0. A
daemon that hosts live runs refuses unless you pass `--even-if-live`, which
ends them first; `--drain` hands them to the next daemon.

One ctxloom version serves at a time. A command of a newer version replaces
an idle daemon of an older one. In every other mismatch (a daemon that hosts
live runs, a daemon newer than the command, and always `ctxloom mcp serve`
and the hooks, which never stop a daemon) the command is refused, exit 20:
`the serving daemon is ctxloom X, and this mcp client is ctxloom Y: one
ctxloom version serves at a time`. The fix is `ctxloom daemon stop`; the next
command starts the daemon of the version you are running. (Pinned by tests;
showing it by hand needs two builds.)

### Messaging a session: `session send`

`ctxloom session send <session> <urgency> <text>` takes one of three
urgencies:

- `inform`: read at the session's next turn; it never starts one.
- `wake`: start a turn when the session is idle (waits while one is running).
- `steer`: interrupt the turn in flight and run next.

A hosted child takes all three. A primary session (the one running in your
terminal) takes `inform` and `wake`, never `steer`. A wake reaches a primary
only while its ctxloom MCP server is connected (a session started with
`--no-context` has none): otherwise it is refused, `daemon-api-wake-inform-only`,
and `inform` is the fix. A wake is a nudge: the messages are delivered when the
session's next prompt starts, as added context. Any other session takes an
`inform` alone.

### Doctor and credentials

`ctxloom doctor` checks that each engine in use has a credential. When the
interactive session can still sign in with the engine's own login but a child
agent run is refused (no token in the environment), it is a warning, not a
failure, and its fix names the variables to export (for claude-code, `claude
setup-token` makes `CLAUDE_CODE_OAUTH_TOKEN`).

## Licence

Apache-2.0 OR BSD-3-Clause, at your option: see [LICENSE-APACHE](LICENSE-APACHE) and
[LICENSE-BSD](LICENSE-BSD).
