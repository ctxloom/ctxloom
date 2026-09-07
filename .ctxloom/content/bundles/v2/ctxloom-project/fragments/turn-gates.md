---
description: You are choosing which build, test, lint, or mutation command to run; interpreting an exit code or a suite's output; or deciding whether the gate you ran actually covers the claim you are making. For anyone deciding which command proves a change is done. This is about WHICH gate and how to read it, not about whether a passing test means anything.
tags:
  - ctxloom
  - testing
  - workflow
---
# Which gate is real in this repo, and how to read one

Companion to green-is-not-passing, which says a test is not passing until a
mutation dies. This says which command actually proves that here, and how to
read what it tells you.

## The gates, and what each one is worth

    just build                    ~4s
    just lint                    ~19s
    just test-arch               ~20s   the architectural class gates, -tags arch
    just test-integration        ~30s
    just test-acceptance        ~510s   THE REAL GATE
    just test-acceptance-focus features/<f>.feature   ~10-17s, one feature

`just test` only COMPILES the integration suite. It is not the gate, and a green
one proves far less than it looks like it does.

Iterate on the narrow target; run `test-acceptance` before claiming anything is
done. When you need a single acceptance scenario — hand-mutating one, say —
`test-acceptance-focus` runs a whole feature in seconds, so acceptance mutation
is cheap and there is no excuse for skipping it.

## Read the EXIT CODE — and know where that is not enough

Gate on the exit status, never on grepping output for "PASS" or "ok". Then know
the three inversions, each of which reports success while telling you otherwise:

- `gofmt -l` LISTS unformatted files and exits 0. Check whether the OUTPUT is
  empty, not the status.
- `go test -run <pattern>` whose pattern matches NOTHING exits 0 having run no
  test. Confirm it actually ran.
- a pipeline reports the LAST command's status, so `cmd | tail; echo $?` gives
  tail's exit. Redirect to a file and check directly, or use PIPESTATUS.

The shape to watch for throughout: exit 0, a success message, and zero bytes
written.

## Which mutation method reaches which code

gremlins is COVERAGE-GATED: it mutates source and runs `go test`. The acceptance
suite and the testenv integration tests exec a PRE-BUILT binary, and Go coverage
does not cross an exec boundary — so those mutants report NOT COVERED and are
never attempted. There, HAND mutation is the only real method, not a poor
substitute.

    unit-testable code      just test-mutation-diff <BASE>
    anything via the CLI    hand mutation

HAND MUTATION MUST REBUILD. Those suites exec a previously built binary and
`just test-pkg` does not rebuild. Edit production code, run `just build`, THEN
the gate. Skip the build and the mutation survives against the old binary — you
will call a good test vacuous, or a vacuous one fine.

When a mutation SURVIVES, suspect you aimed at the wrong layer before concluding
the test is vacuous. The tell is that the mutated code is demonstrably live
elsewhere: if breaking it reddens some other package's tests, the code is
reachable, just not from the path under test. Find the reachable gate and kill
that instead.

## A fresh worktree cannot commit until it is built

`just build` first, and make sure `bin/archlint` exists there. Without a built
tree the pre-commit hook fails with an `archvocabulary: failed prerequisites`
cascade that looks like a broken analyzer and is not.

`test-pkg` takes a package PATTERN — `./internal/cli`, with the leading `./`.
Without it you get a misleading "is not in std" error.
