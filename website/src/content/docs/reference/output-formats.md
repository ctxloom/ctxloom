---
title: "Output Formats"
---

Every `ctxloom`, `taskloom`, `ltk` and `harp` command that prints a result
takes the same global `--format` flag.

| Format | What you get |
|--------|--------------|
| `text` | Readable output for a terminal |
| `json` | One JSON document, for `jq` and scripts |
| `yaml` | The same document as YAML, two-space indented |
| `toml` | The same document as TOML |
| `markdown` | The readable view as Markdown: headings, `**Label:** value` lines and tables |

`yml`, `txt` and `md` are accepted as aliases, in any case.

## The default follows your terminal

With no `--format`, output is `text` when it goes to a terminal and `json` when
it is piped or redirected, so `taskloom list | jq …` works without the flag.
Pass `--format text` to keep the readable form in a pipe. `--json` is
shorthand for `--format json` on commands that carry it.

## One document, three encodings

`json`, `yaml` and `toml` carry the same fields under the same names, so a
script can switch between them. A few rules hold across commands:

- An empty list is `[]`, never `null`.
- `json` and `yaml` list keys in the same order. `toml` lists them
  alphabetically, because TOML output has no ordered form.
- An empty result in `text` or `markdown` prints `(none)` (`# (none)` in
  `toml`) rather than nothing, so "nothing to show" is never confused with
  "nothing was written".

## Errors and warnings

Results go to stdout; failures and warnings go to stderr.

- A failure exits non-zero. If you **asked** for `--format json`, `yaml` or
  `toml`, stderr carries a parseable error document,
  `{"error": "…", "remedy": "…"}` (`remedy` only when there is a suggested
  fix). Otherwise stderr carries one readable line: `taskloom`, `ltk` and
  `harp` print `<program>: <message>`, and `ctxloom` prints
  `Error: <message>`, followed in both cases by a `fix: …` line when there is
  a suggested fix.
- A format chosen only because output was piped does not change stderr: you
  still get the readable line, so a redirected run's errors stay readable.
- Under an explicit or piped `json`, `yaml` or `toml`, warnings are JSON Lines,
  one `{"prog": "…", "warning": "…"}` object per line (with `remedy` when there
  is one). Otherwise they read `<program>: warning: <message>`.
