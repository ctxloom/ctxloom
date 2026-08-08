# Trying ctxloom without betting the repo

Adopting a tool that rewrites your assistant's configuration is a small act of
trust. You are letting something edit files you did not write, in a project you
depend on, to change how a tool you already rely on behaves. The reasonable
response is caution, and the reasonable question is not "is this good?" but
"can I get out?"

ctxloom's answer is that the exit is part of the product. Wiring in is one
command, taking it back out is one command, and what you authored is never
ctxloom's to remove.

## What this proves

Alice has a live repo and five assistants configured five different ways. She
tries ctxloom the way anyone sensible tries a new tool — she wires it in, gets
suspicious ten minutes later that it did anything at all, and then, *before*
committing any of it, takes it back out again to see what is left behind.

Each scenario below is one beat of that hour:

- **Wiring in** — after one command, a fresh assistant session receives the
  project's context automatically. Nobody copies a prompt anywhere.
- **Verifying** — the tool's own report has to agree with what is on disk. A
  status command that says "wired" over a project where nothing was written is
  worse than no status command at all, because it answers exactly the question
  being asked.
- **Checking without a network** — `ctxloom doctor` is deterministic. No
  network, no engine, no credentials. It is the check you can run on a train,
  and the one you reach for when you do not yet trust the tool.
- **Leaving** — the half that decides adoption. Uninstall removes what ctxloom
  wired into the engine and leaves the project's own content exactly where it
  was.

## What it deliberately does not prove

Every assertion here names something *Alice* can see. The byte-level proofs —
which key in which settings file, how a SessionStart hook is told apart from a
statusline entry, what each engine's own config surfaces look like — belong to
the `manage` command's own reference page, not to this story.

That split has teeth. The reference owns the exact proofs precisely because
they are exacting — telling the context hook apart from the statusline entry
needs a pattern, not a substring, since the two overlap textually. Keeping that
rigour in one place is what lets this page stay a story without either page
proving less.
