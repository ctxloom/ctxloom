# 31 — Rename map (slice 0b)

One row per package `go list ./internal/... ./cmd/...` printed at the base of the rename slice. The left column is the path before the move; the right column is the path after it, or `dies in place` for a package the rename paragraph of `30-decided-architecture.md` §1.1 retires (it is deleted, not moved, by the slice named in its source), or `stays` for a package the rings do not restructure.

This table is CHECKED, not prose: `TestArch_RenameMap_LeftColumnGone` and `TestArch_Rings_EveryPackageInsideARing` in `tests/arch` read it and fail if any left-column path is still a package, if any right-column path is not one, or if any package sits outside `internal/core/`, `internal/adapters/`, `internal/engines/`, `internal/shared/`, the family products, `internal/testsupport/` and `cmd/` without a `dies in place` row here. A `JUDGMENT` source is a placement none of the design's sources decided; it is the rename's call and open to reversal by a later slice.

| Today | Target | Ring | Source of the placement |
|---|---|---|---|
| `internal/agentcoord` | `internal/adapters/coordgrpc/pb` | adapters | rename paragraph: the generated proto → adapters/coordgrpc/pb |
| `internal/agentcoord/coord` | `internal/core/coord` | core | rename paragraph: agentcoord/coord → core/coord |
| `internal/agentcoord/coord/coordtest` | `internal/adapters/runner/coordtest` | adapters | slice 14a (5): the in-process runner double moves beside the runner it stands up |
| `internal/agentcoord/discover` | `internal/adapters/coordgrpc/discover` | adapters | Part 1.1: discover moves to adapters/coordgrpc (slice 10) |
| `internal/agentcoord/mcpschema` | `internal/adapters/coordgrpc/mcpschema` | adapters | JUDGMENT: Part 4.1 row 10 folds mcpschema into adapters/coordgrpc; until then it sits beside the proto it binds |
| `internal/agentcoord/mcpschema/gen` | `internal/adapters/coordgrpc/mcpschema/gen` | adapters | subpackage keeps its relative path under mcpschema |
| `internal/agentcoord/spool` | `internal/core/spool` | core | rename paragraph: agentcoord/spool → core/spool |
| `internal/agents` | `internal/adapters/agents` | adapters | JUDGMENT: imported by core (config, coord) only through edges unit A allowlists as leaving in slices 4 and 8; a domain type, so not toolbox → adapters |
| `internal/archlint` | `internal/shared/archlint` | shared | JUDGMENT: domain-free leaf (go/analysis tooling; no in-repo imports) → toolbox |
| `internal/buildpins` | `internal/shared/buildpins` | shared | JUDGMENT: domain-free leaf (test-only pin gates; no in-repo imports) → toolbox |
| `internal/bundles` | `internal/core/bundles` | core | unit A core row (core-imports-only-core from list); package table core/bundles |
| `internal/claude` | `internal/engines/claude` | engines | rename paragraph: claude → engines/claude |
| `internal/claude/engine` | `internal/engines/claude/engine` | engines | subpackage keeps its relative path under engines/claude |
| `internal/cli` | `internal/adapters/cli` | adapters | unit A adapters row; package table adapters/cli |
| `internal/cli/tui` | `internal/adapters/cli/tui` | adapters | subpackage keeps its relative path under adapters/cli; package table cli/tui |
| `internal/compression` | `internal/shared/compression` | shared | JUDGMENT: domain-free leaf (text/AST compression; imports only the toolbox) → toolbox |
| `internal/config` | `internal/core/config` | core | unit A core row; package table core/config |
| `internal/config/layerscope` | `internal/adapters/configload/layerscope` | adapters | rename paragraph: config/layerscope → adapters/configload/layerscope |
| `internal/confpatch` | `internal/adapters/confpatch` | adapters | unit A adapters row; package table adapters/confpatch |
| `internal/content` | `internal/adapters/content` | adapters | JUDGMENT: imported by core only through edges unit A allowlists as leaving in slices 4 and 5 (readers become adapters); not domain-free → adapters |
| `internal/content/archive` | `internal/adapters/content/archive` | adapters | subpackage keeps its relative path under adapters/content |
| `internal/content/attest` | `internal/adapters/content/attest` | adapters | JUDGMENT: unit A adapters row; the package table names adapters/attest but the rename paragraph's subpackage rule (relative path kept) governs tonight — slice 5 may hoist it |
| `internal/content/convert` | `internal/adapters/content/convert` | adapters | subpackage keeps its relative path under adapters/content |
| `internal/content/remotetree` | `internal/adapters/content/remotetree` | adapters | subpackage keeps its relative path under adapters/content |
| `internal/contextmetrics` | `internal/adapters/contextmetrics` | adapters | JUDGMENT: persists per-session samples on disk (a store); imported by cli and mcp → adapters |
| `internal/docsgen` | `internal/shared/docsgen` | shared | JUDGMENT: domain-free leaf (doc generator; no in-repo imports) → toolbox |
| `internal/enginepins` | `internal/shared/enginepins` | shared | JUDGMENT: domain-free leaf (test-only pin gate; no in-repo imports) → toolbox |
| `internal/engineversion` | `internal/adapters/engineversion` | adapters | JUDGMENT: runs the engine CLI to ask its version (a process adapter); retired at slice 6b → adapters |
| `internal/errs` | `internal/shared/errs` | shared | Part 0 toolbox list; rename paragraph: the top-level toolbox packages → internal/shared/ |
| `internal/git` | `internal/adapters/git` | adapters | JUDGMENT: the git exec seam (drives a process); not domain-free → adapters |
| `internal/gitignore` | `internal/adapters/gitignore` | adapters | JUDGMENT: writes the project's ignore files (a filesystem store); imports the ledger → adapters |
| `internal/liveness` | `internal/shared/liveness` | shared | Part 0 toolbox list; rename paragraph: the top-level toolbox packages → internal/shared/ |
| `internal/lm/backends` | `dies in place` | retired | rename paragraph: retired (slice 11b); dies in place |
| `internal/lm/conformance` | `internal/engines/conformance` | engines | JUDGMENT: test-only cross-engine equity suite over the engine implementations → engines ring |
| `internal/lm/hosting` | `dies in place` | retired | slice 6b split lm/engine: the declarative Descriptor became core/engine.Definition and this is its hosting remainder (the instance half lm/backends still runs); retired with lm/backends (slice 11b); dies in place |
| `internal/lm/engines` | `internal/engines` | engines | rename paragraph: lm/engines → engines (the registry build) |
| `internal/lm/grpc` | `dies in place` | retired | rename paragraph: retired (slice 13); dies in place |
| `internal/lm/isolation` | `internal/adapters/isolation` | adapters | rename paragraph: lm/isolation → adapters/isolation |
| `internal/ltk/app` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/engine` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/frontend` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/frontend/cmd` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/frontend/pwsh` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/frontend/shell` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/ir` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/rules` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/scm` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/shellenv` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/state` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/ltk/tools/extract-defaults` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/mcp` | `internal/adapters/mcp` | adapters | JUDGMENT: unit A adapters row; Part 1.1 says internal/mcp stands for runner/mcp, and adapters/runner is born at slice 8 — leaf name kept until then |
| `internal/memory` | `internal/adapters/memory` | adapters | unit A adapters row; package table adapters/memory |
| `internal/mockengine` | `internal/engines/mock` | engines | rename paragraph: mockengine → engines/mock |
| `internal/operations` | `internal/adapters/operations` | adapters | unit A adapters row; package table adapters/operations |
| `internal/paths` | `internal/core/paths` | core | unit A core row; package table core/paths |
| `internal/profiles` | `internal/core/profiles` | core | unit A core row; package table core/profiles |
| `internal/projectroot` | `internal/adapters/projectroot` | adapters | JUDGMENT: finds the root on disk and through git (env-literals-once names it a permitted reader); imported by core only through an edge unit A allowlists as leaving in slice 4 → adapters |
| `internal/refuri` | `internal/shared/refuri` | shared | Part 0 toolbox list; rename paragraph: the top-level toolbox packages → internal/shared/ |
| `internal/remote` | `internal/adapters/remote` | adapters | unit A adapters row; package table adapters/remote |
| `internal/schema` | `internal/shared/schema` | shared | Part 0 toolbox list; rename paragraph: the top-level toolbox packages → internal/shared/ |
| `internal/schemagen` | `internal/shared/schemagen` | shared | JUDGMENT: domain-free leaf (schema reflector, build-tagged; no in-repo imports) → toolbox |
| `internal/selfexec` | `internal/adapters/selfexec` | adapters | JUDGMENT: resolves the running binary's path (process/OS facts); imports clidiag → adapters |
| `internal/sessions` | `internal/core/sessions` | core | unit A core row; package table core/sessions |
| `internal/shared/admission` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/agent` | `internal/core/agent` | core | JUDGMENT: unit A core row (core-imports-only-core from list); the package table has no core/agent — slice 6b folds it into core/engine, so it keeps its leaf name under core until then |
| `internal/shared/agent/present` | `internal/core/present` | core | rename paragraph: shared/agent/present → core/present |
| `internal/shared/clidiag` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/cliemit` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/cliversion` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/collections` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/companionloadout` | `internal/adapters/companions` | adapters | rename paragraph: shared/companionloadout → adapters/companions |
| `internal/shared/confload` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/containerprobe` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/doccapture` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/envswitch` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/gitutil` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/harp` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/harpmarker` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/iox` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/keymatch` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/ledger` | `dies in place` | retired | rename paragraph: retired (slice 12); dies in place |
| `internal/shared/lockwait` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/logsink` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/mountns` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/pidalive` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/plans` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/procsec` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/ptyrunner` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/realpath` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/schemaver` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/sessionlock` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/shellenv` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/stderrtail` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/strictness` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/tasks` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/lint` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/operations` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/paths` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/priority` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/projectid` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/tagschema` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/taskstest` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/tasks/triggers` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/shared/termsafe` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/textutil` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/tokens` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/upgrade` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/watch` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/shared/wire` | `internal/core/wire` | core | rename paragraph: shared/wire → core/wire |
| `internal/shared/yamlx` | stays | shared | JUDGMENT: already under the toolbox prefix and not one of the shared packages the rename paragraph moves out; Part 0 lists it as neither toolbox nor ring member, so a later slice decides it |
| `internal/signing` | `internal/adapters/signing` | adapters | unit A adapters row; package table adapters/signing/* |
| `internal/signing/agentkey` | `internal/adapters/signing/agentkey` | adapters | subpackage keeps its relative path under adapters/signing |
| `internal/signing/allowedsigners` | `internal/adapters/signing/allowedsigners` | adapters | subpackage keeps its relative path under adapters/signing |
| `internal/signing/countersign` | `internal/adapters/signing/countersign` | adapters | subpackage keeps its relative path under adapters/signing |
| `internal/taskloom/config` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/taskloom/engine` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/taskloom/workdir` | stays | — | Part 0: a family product; this document does not restructure them |
| `internal/termui` | `internal/adapters/termui` | adapters | unit A adapters row; package table termui |
| `internal/testsupport` | stays | — | Part 0: test-only, outside the rings |
| `internal/testsupport/containercell` | stays | — | Part 0: test-only, outside the rings |
| `internal/testsupport/dockergate` | stays | — | Part 0: test-only, outside the rings |
| `internal/testsupport/enginefixture` | stays | — | Part 0: test-only, outside the rings |
| `internal/testsupport/parity` | stays | — | Part 0: test-only, outside the rings |
| `internal/testsupport/procalive` | stays | — | Part 0: test-only, outside the rings |
| `internal/testsupport/sourcedir` | stays | — | Part 0: test-only, outside the rings |
| `internal/tmuxhost` | `internal/adapters/tmuxhost` | adapters | JUDGMENT: hosts processes in tmux (a process adapter); retired with vpio at slice 13 → adapters |
| `internal/transcript` | `internal/adapters/transcript` | adapters | unit A adapters row; package table adapters/transcript/* |
| `internal/transcript/policy` | `internal/adapters/transcript/policy` | adapters | subpackage keeps its relative path under adapters/transcript |
| `internal/transcript/vendorreader` | `internal/adapters/transcript/vendorreader` | adapters | subpackage keeps its relative path under adapters/transcript |
| `internal/transcript/vendorreader/claude` | `internal/adapters/transcript/vendorreader/claude` | adapters | subpackage keeps its relative path under adapters/transcript |
| `internal/transcript/vendorreader/mock` | `internal/adapters/transcript/vendorreader/mock` | adapters | subpackage keeps its relative path under adapters/transcript |
| `internal/trust` | `internal/core/trust` | core | unit A core row; package table core/trust |
| `internal/turnchange` | `internal/adapters/turnchange` | adapters | JUDGMENT: reads the engine's transcript through the vendor readers (an adapter over adapters); imported by cli → adapters |
| `internal/version` | `internal/shared/version` | shared | JUDGMENT: a leaf with no ctxloom imports by its own doc comment → toolbox |
| `internal/vpio` | `internal/adapters/vpio` | adapters | JUDGMENT: unit A adapters row; the rename paragraph's vpio/* → adapters/hostpty, adapters/attach is a split of one package into two (a code change, slice 13's), so the leaf name is kept |
| `internal/vpio/goplugin` | `dies in place` | retired | rename paragraph: retired (slice 13); dies in place |
| `cmd/archlint` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/ctxloom` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/gen-schemas` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/harp` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/ltk` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/mockengine` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/probe-mcp-server` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/taskloom` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
| `cmd/validate` | stays | — | Part 0: cmd/* are the composition roots; they do not move |
