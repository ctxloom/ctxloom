# Session layout, mounts and secrets

Every ctxloom session (a harp) owns two places on disk: a **machine session dir** under the
ctxloom home, holding what ctxloom needs to run, resume and reclaim the session, and an
**output dir** in the human's own documents, holding what a person reads. This page is the
design: what lives where, what a container sees, what is deleted when, and why.

The authorities are code, not this page:

- `paths.HarpMembers` (internal/core/paths/harpmembers.go) is the session dir's table — every
  walker, reaper and container mount list derives from it (`paths.ClassifyMember`,
  `paths.MountedMembers`, `sessions.ReapPolicy.Members`).
- `sessions.Entry.OutputDir` (the `output_dir` key of `session.yaml`) is where one session's
  outputs are; `paths.DefaultOutputBase` and `paths.OutputDir` compose the default.
- `isolation.Container.sessionStateMounts` and `containerRelocator.relocate` build the
  container's view.

## Data classes

| Class | Lives | Lifetime | Example |
|---|---|---|---|
| Identity | session dir, top | until the session is swept (a `keep` marker exempts it) | `session.yaml`, `keep` |
| Machine, durable | session dir | while the session is resumable; the sweep takes it | `spool/`, `package/`, `native/`, `transcripts/`, `diagnostics.log`, `context-metrics.jsonl` |
| Machine, disposable | session dir | rebuilt or recreated per launch | `home/` (engine config homes) |
| Work | session dir | kept while it holds work; triaged, never removed blind | `work/` (worktree checkouts) |
| Scratch | session dir | one run | `scratch/` (`ctxloom-iso-*`, `ctxloom-tmp-*`, the secret dir's disk fallback) |
| Readable output | output dir | the human's; ctxloom's sweeps never delete it | `essence.md`, `next-step.md`, `*.plan.md`, `segments/<id>.md`, `reports/` |

## Layout

```
~/.ctxloom/sessions/<harp>/                 machine session dir (paths.HarpDir)
  session.yaml                              identity; records project_dir and output_dir
  keep                                      hand-placed sweep exemption
  diagnostics.log                           a TUI session's diverted warnings
  context-metrics.jsonl                     context-occupancy series (statusline hook)
  spool/                                    mail: in/, out/, out/consumed/, in/delivered/, ...
  package/<digest>                          claim-checked launch packages
  transcripts/
    transcript.jsonl                        ctxloom's canonical transcript
    segments/<id>.jsonl, <id>.watermark.json  per-rotation raw segments
  native/<leaf>/<history>/...               each engine's native history (claude: native/claude/projects/<slug>/<id>.jsonl)
  home/<leaf>/                              disposable engine config home (CLAUDE_CONFIG_DIR)
    <history> -> ../../native/<leaf>/<history>   RELATIVE link
  work/ctxloom-wt-*                         worktree checkouts
  scratch/ctxloom-iso-*, ctxloom-tmp-*, ctxloom-secret-*

<Documents>/ctxloom/<project>/<harp>/       output dir (session.yaml output_dir)
  essence.md, next-step.md, *.plan.md
  segments/<id>.md
  reports/<agent harp>/                     each agent's FINAL report in this tree
    report.md                               the report's text
    <artifact name>                         each artifact it published, latest revision
```

A FINAL `agent_report` from any agent in the tree is saved into the ROOT session's output
dir as it is journaled (`Coordinator.saveFinalReport`): the text as `report.md` and each
artifact it names, at its latest revision, under its base name — prefixed with its artifact
id when another file of the report already took that name. A later FINAL rewrites the
files it names. Saving is best-effort: the report is journaled either way.

`<leaf>` is the engine's session-home leaf (`launch.SessionHome`); `<history>` is the
engine's `engine.HomeSpec.TranscriptStoreRel` (claude: `projects`). `launch.NativeHome`
places `native/<leaf>` at the same depth as `home/<leaf>`, which is what lets one relative
link resolve everywhere.

### The output dir

The default base is `<Documents>/ctxloom` (`paths.DefaultOutputBase`): Documents is
`XDG_DOCUMENTS_DIR` from `user-dirs.dirs` on Linux and other XDG systems, else
`~/Documents`; `~/Documents` on macOS; the `FOLDERID_Documents` known folder on Windows.
The `output_dir` machine config key replaces the base (absolute paths only,
`operations.ErrRelativeOutputDir`). Below the base the dir is `<project>/<harp>`, where
`<project>` is the project directory's plain name.

The absolute path is recorded at mint (`Store.RecordOutputDir`, called by
`operations.MintIdentity` and `operations.AssignSessionHarp`) and is the answer from then
on (`sessions.OutputDir`): the default depends on per-user and per-platform state and the
base is configurable, so re-deriving it later could name a folder the session never wrote
to. A process inside a container reaches it at `/ctxloom/out`, which `CTXLOOM_OUTPUT_DIR`
names; `sessions.OutputDirIn` prefers that variable, then the record.

Renaming a session (`session edit --name`, `sessions.Manager.Rename`) moves its output dir to
the new name beside the old one and records the new path. Something already there
(`sessions.ErrOutputDirExists`) or a move that fails (`sessions.ErrOutputDirMove`) refuses the
rename, and every step taken is undone. An editor or sync client holding the old folder sees
it move.

## Container mounts

| Host | Container | Mode | Built by |
|---|---|---|---|
| the project, or the worktree checkout | the runtime mapper's target | rw | `containerRelocator.relocate` |
| git common dir, worktrees mask, pointer files | same paths | rw / ro / ro | the container bases |
| `home/<leaf>` | `/ctxloom/home/<leaf>` | rw | `containerRelocator.relocate` |
| `native/<leaf>` | `/ctxloom/native/<leaf>` (beside the instance root) | rw | `containerRelocator.relocate` |
| each `paths.MountedMembers` row | `~/.ctxloom/sessions/<harp>/<rel>` in the container home | rw | `Container.harpStateMounts` |
| the output dir | `/ctxloom/out` (`CTXLOOM_OUTPUT_DIR`) | rw | `Container.outputMounts` |
| this project's task log and its `.lock` | same paths in the container home | rw | `Container.taskStoreMounts` |
| `scratch/<run>/locks` and per-in-place-file host locks | `~/.ctxloom/locks` | rw | `Container.lockMounts` |
| config overlays `scratch/<run>/cfg<N>` | project-relative config dirs | rw | `containerConfigOverlay` |
| the run's secret dir | `/run/ctxloom/secrets` | ro | `Container.bind` |

The Mounted rows are the members a containerized run writes or reads at their own paths:
the spool (mail), the package store (the runner redeems claim-checked launches), the
transcripts dir (the runner records the canonical transcript) and the context-metrics file
(the engine's statusline hook appends it). A file row's bind source is created as a file
first (`HarpMember.File`), because a runtime asked to bind a missing source creates a
directory in its place.

## Deletion matrix

| Class | Run cleanup | Coordinator Close | `clean` (reclaim) | `clean --include-persist` | `session sweep` purge |
|---|---|---|---|---|---|
| `scratch/`, secret dirs | delete | delete (owner and every ended child) | delete | delete | — |
| `home/<leaf>` | keep | delete (owner and every ended child) | delete | delete | — |
| `work/` checkouts | removed when clean, kept with work (existing triage) | — | triaged | triaged | triaged |
| `spool/`, `package/`, `diagnostics.log`, `context-metrics.jsonl` | keep | keep | keep | delete (distilled only) | keep |
| `native/`, `transcripts/` | keep | keep | keep | delete (distilled only) | delete (distilled, or an internal one-shot) |
| `session.yaml`, `keep` | keep | keep | keep | keep | keep |
| coordinator root | keep | delete only if ephemeral and settled | — | — | removed with the session |
| output dir | keep | keep | never | never | never |

Close's rule lives in `Coordinator.removeDisposableMembers`; the reaper's in
`sessions.ReapPolicy.Members`; the purge populations in `operations.PurgeSession`, of which
the sweep requests only the transcript population. The output dir is reachable by exactly
one destroyer, the explicit `ctxloom session artifacts purge` (`outputEssenceItems`).

## Secrets

Each run has ONE secrets file: dotenv, written by `github.com/joho/godotenv`
(`sessions.EncodeSecrets`), owner-only, in a per-run secret dir (`secretsFile`). The dir
lives on the platform's per-user tmpfs (`platform.PrivateTmpfs`: `$XDG_RUNTIME_DIR` on
Linux) when there is one; otherwise it falls back to disk under the session's `scratch/`,
which is allowed but announced — once at launch (`isolation.SecretsOnDiskNotice`) and by
`DOCTOR-CHECK-SECRETS-STORAGE-k1`, both naming the platform and the place.

The writer proves every value by decoding the file back and refuses one that would not
return byte for byte (`sessions.ErrSecretNotRoundTrippable`, naming the variable, never the
value). godotenv cannot hold a value ending in a backslash or in a double quote, and writes
an integer unquoted (a leading zero is lost); every `$` is escaped, so reading expands
nothing.

A container run's engine credentials go into the file, mounted read-only at
`/run/ctxloom/secrets`; the Placement names the file for each variable
(`launch.Placement.SecretFiles`) and the runner reads them out of it (`redeemSecrets`).

The coordinator credential reaches EVERY runner, host and container, through the same
file: the environment that starts the runner moves it out of the spawn request
(`stageCoordCred`) and names the file in `CTXLOOM_COORD_CRED_FILE` — the mounted path for a
container, the host path for a host runner. `sessions.DecodeReach` reads the credential
from that file only; `CTXLOOM_COORD_CRED` is never a variable in any process's
environment, and a host runner start that still carries it is refused
(`errCredInExecEnv`).

## Rulings and why

- **Native history is kept out of the disposable home.** The engine's config home is
  rebuilt every launch and deleted on Close, so the conversation history it accumulates
  would die with it. The home holds a relative link into `native/` instead; a binding made
  through the link is recorded with symlinks resolved (`sessions.BindSession`), so it stays
  true after the home is gone. Only the history store moves: everything else in the home
  (claude's `file-history/`, `history.jsonl`, settings) stays disposable.
- **The link is relative, and native/ mounts beside the home.** A container mounts
  `home/<leaf>` and `native/<leaf>` at different absolute paths than the host, so only a
  relative link can resolve on both sides. Placing `native/<leaf>` at the same depth as
  `home/<leaf>`, on both sides, is what makes `../../native/<leaf>/<history>` correct
  whatever the instance root is named.
- **The session dir is machine state; readable outputs go to the output dir.** A human
  looking for a plan or an essence should find it where they keep documents, and nothing
  that reclaims machine state should be able to take it.
- **The output dir path is recorded, not re-derived.** See "The output dir".
- **The sweep never deletes the output dir.** It is the human's; only the explicit
  artifacts purge reaches into it.
- **Close cleans every finished agent in the tree.** A finished agent's home and scratch
  are pure cost; an agent whose run has not ended (an adopted run whose runner never came
  back) may still have an engine using its home, so it is left for the sweep.
- **No migration.** Sessions created under an earlier layout are not converted; a session
  home whose history dir is a real directory is refused (`isolation.ErrHistoryNotLinked`)
  rather than adopted.

## Live evidence

The two vendor facts the native-history design stands on are probe cells, re-run on every
claude pin bump (`features/probes/capability_native_history.feature`, judges in
`tests/acceptance/probe_p14_native_history.go`): claude in a rootless container writes its
conversation `.jsonl` under its config home's `projects/`, and claude writes through a
relative symlinked `projects/` without replacing the link. Their current results are in
the probe registry (`probeP14` in `tests/acceptance/capability_probe_registry.go`).

## Limits

- **Resume across a runtime switch.** claude keys `projects/<slug>` by the working
  directory. A container mounts the project at the host's absolute path (the P14 container
  cell observed the host path as the slug), so host and container runs of one session file
  history under the same slug. A worktree checkout is a different directory from the
  project, and so a different slug, on either runtime.
- **Windows.** The history link is a directory junction (`platform.DirLinker`), which
  needs no privilege but names its target absolutely, so it does not resolve inside a
  container. A host run links as everywhere else; a container run keeps its history in the
  mounted session home instead (`nativeHomeFor`), where it is deleted when the session
  closes, and says so once at launch.
- **A missing output dir.** Readers treat a missing directory as "nothing written yet". A
  session whose record has no output dir is reported (`sessions.ErrNoOutputDir`); a
  container run of one is refused.
