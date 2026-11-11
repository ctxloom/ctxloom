# ctxloom's built-in bundle

ctxloom ships one bundle in the binary, `ctxloom` (owner rulings 110 and 111,
narrowed by 117 and 123), compiled in by `library::builtin`. It holds
ctxloom's operational prompts:

- `prompts/setup-interview.md`: the setup interview `ctxloom init` opens
  with and `ctxloom init prompt` prints (once Go ctxloom's
  `resources/commands/ctxloom-init.md`, its front matter dropped);
- `prompts/distill.md`: the distill prompt, the owner's own text (ruling
  123; once ctxloom-default's `skills/distill.yaml` `content`).

Both are the owner's text: they are not edited here without the owner. The
bundle is not generally injected: it carries no fragment, profile or tag,
and no profile includes it; only init and distill use it. The library
catalog lists it under the read-only built-in origin `ctxloom/default`; a
project bundle named `ctxloom` overrides it in the catalog. Nothing is
pulled for it, nothing is written under `.ctxloom/cache`, and it works
offline. Adding a file here means adding it to `library::builtin::FILES`.
