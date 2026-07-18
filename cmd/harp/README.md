# harp

Generate Human Appropriate Random Phraselets — pronounceable, memorable
identifiers of the form `swift-amber-falcon`.

`harp` is a thin, standalone CLI over the same generator
([`internal/shared/harp`](../../internal/shared/harp)) that ctxloom itself
uses in-process for session and task IDs. ctxloom never shells out to this
binary — it imports the library directly — so `harp` exists purely for
ad-hoc and scripted use outside a ctxloom process. It replaces the former
`ctxloom harp` subcommand, extracted here so it can be built, versioned, and
distributed independently of the main `ctxloom` binary.

## Usage

```
harp                          # one name, default 3 components
harp -c 2                     # adj-noun
harp -n 5                     # five names, one per line
harp -g long                  # draw from the long-word group
harp -s _ --max-len 5         # short_hawk style
harp -n 3 --format json       # ["a-b-c", "d-e-f", "g-h-i"]
harp version                  # print the harp binary's own version
```

| Flag | Short | Default | Meaning |
|---|---|---|---|
| `--components` | `-c` | `3` | number of words (2-16) |
| `--separator` | `-s` | `-` | delimiter between words |
| `--max-len` | | `0` (no cap) | max characters per word |
| `--number` | `-n` | `1` | how many names to generate |
| `--group` | `-g` | `default` | word-list group (`default`, `long`) |
| `--format` | | `text` | `json`, `yaml`, `toml`, `text`, or `markdown` |

Output goes through [clifmt](https://github.com/ctxloom/clifmt): the
result is a bare list of names, so `--format json` renders a JSON array,
`--format text` renders one name per line, and `--format markdown` renders
a bullet list — no per-command rendering code, and every format `clifmt`
supports comes for free.

## Build

```
just build              # bin/harp, CGO_ENABLED=0, static
just build-compressed   # bin/harp, then UPX-compressed
just test
just lint
```

From the repo root (composed via `justfile.container`'s `mod harp`):
`just build-harp`, or `just harp::build` inside the devcontainer.

## Release

`harp` ships as its own [goreleaser](../../.goreleaser.yml) build/archive
target, the same way the `ltk` and `taskloom` companions do — see the
`harp` / `harp-upx` build IDs and the `harp` archive entry there.
