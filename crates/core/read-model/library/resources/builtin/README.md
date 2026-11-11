# ctxloom's built-in bundles

These are the bundles ctxloom ships built in (owner rulings 110 and 111). They
are compiled into the binary (`library::builtin`). The library catalog lists
them under the read-only built-in origin `ctxloom/default`. A new project's
seeded `default` profile inherits `ai-developer#profiles/developer` from them.
Nothing is pulled, nothing is written under `.ctxloom/cache`, and they work
offline. A project bundle of the same name overrides one of them, as a whole.

- Source repository: https://github.com/ctxloom/ctxloom-default
- Source commit: `6129ccb9b173f957d05ceb956aa078462f86e697` (2026-10-30)
- Source path: `.ctxloom/content/bundles/v2/<bundle>/`
- Bundles: `ai-developer`, `ai-coding-general`, `testing`, `git`: the bundles
  the `developer` profile resolves through.
- Licence: the source repository's `LICENSE` (BSD 3-Clause), copied beside
  this file at the same commit.

The files are a byte-for-byte copy of the source at that commit, and they are
the owner's text: they are not edited here. `SHA256SUMS` lists each one, and
`library`'s tests hold the compiled table to it. The ctxloom workspace's tests
hold this directory to it too.

## Refreshing

1. Copy each bundle out of the source repository at the new commit:
   `git archive <commit> .ctxloom/content/bundles/v2/<bundle> | tar -x -C . --strip-components=4`.
2. Run `find . -type f ! -name LICENSE ! -name SHA256SUMS ! -name README.md | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum > SHA256SUMS`.
3. Bring `library::builtin::FILES` and `SOURCE_COMMIT`, and this file, to
   match.
