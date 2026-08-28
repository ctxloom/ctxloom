# You are this agent. What guidance do you need for your next action?

## How to read what follows

The SESSION HISTORY below is BACKGROUND. It is the recent history of a session
YOU were running -- assume you are that agent, mid-task, with all of this behind
you. It is there so you understand your situation. It is NOT a list of topics to
match against.

The final line, marked [ABOUT TO DO], is the action you are ABOUT TO TAKE. That
action is what you are selecting guidance for. Work already finished earlier in
the history is done; do not pull in guidance for it.

## Choosing

Each fragment below has a PREMISE (the condition under which it applies) and TAGS
(the artifacts and concepts it concerns). Both are valid grounds for including it:
  - If the PREMISE describes what you are about to do, include it.
  - If the TAGS clearly match it, that is also sufficient, even on a near-miss.
  - On a BORDERLINE call, include. An unnecessary fragment costs a little context;
    one you withhold you will never learn existed and cannot ask for.
  - If neither reaches your next action, answer NONE.

Judge each fragment on its own. Several may apply; you are not picking a best match.

Output ONE line, nothing else:

    SELECTED: <comma-separated fragment names, or NONE>

## Session history (background)

[user] ok, lets continue with acceptance tests

[result] (tool result omitted)

[assistant] (ran Bash: List feature files and recent commits)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Find recent sessions and plan files)

[result] (tool result omitted)

[assistant] (ran Bash: Show the J15 wiring task details)

[result] (tool result omitted)

[assistant] (ran Bash: Check draft features and mock container profile state)

[result] (tool result omitted)

[assistant] (ran Bash: Locate containerProfileFor and show uncommitted diff stat)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Inspect containercell and acceptance tag config)

[result] (tool result omitted)

[assistant] (ran Bash: Show mock container profile commit)

[result] (tool result omitted)

[assistant] (ran Bash: Review uncommitted acceptance changes)

[result] (tool result omitted)

[assistant] (ran Bash: Show acceptance just targets)

[result] (tool result omitted)

[assistant] (ran Bash: Read test-acceptance target)

[result] (tool result omitted)

[assistant] (ran Bash: Run narrow acceptance gate on mcp_tools.feature)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run gate capturing exit code and scenario count)

[result] (tool result omitted)

[assistant] (ran Bash: Probe container check/build output and docker availability)

[result] (tool result omitted)

[assistant] (ran Bash: Check real exit codes for container subcommands)

[assistant] (ran Bash: Read renderContainerCheck)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Read j9 container gate and mock container profile)

[result] (tool result omitted)

[assistant] (ran Bash: Find container run E2E tests and in-container detection)

[result] (tool result omitted)

[assistant] (ran Bash: Inspect mock record mechanism)

[result] (tool result omitted)

[assistant] (ran Bash: Read mock record format)

[result] (tool result omitted)

[assistant] (ran Bash: Read recordMockInput body)

[result] (tool result omitted)

[assistant] (ran Bash: Check import direction between backends and isolation)

[result] (tool result omitted)

[assistant] (ran Bash: Measure ctxloom container build mock)

[result] (tool result omitted)

[assistant] (ran Bash: Read isolation probe target and feature)

[result] (tool result omitted)

[assistant] (ran Bash: Measure warm rebuild and check image presence)

[result] (tool result omitted)

[assistant] (ran Bash: Read container-build-acceptance / live-container targets)

[result] (tool result omitted)

[assistant] (ran Bash: Smoke-test a containerized mock run)

[result] (tool result omitted)

[assistant] (ran Bash: Inspect run log head and recent containers)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run with record file inside the workspace, timed)

[result] (tool result omitted)

[assistant] (ran Bash: Containerized mock run from the project dir, timed)

[result] (tool result omitted)

[assistant] (ran Bash: Look for container spawn evidence in the structured log)

[result] (tool result omitted)

[assistant] (ran Bash: Watch docker events during a containerized mock run)

[result] (tool result omitted)

[assistant] (ran Bash: Check image tag from the scratch project and list agent images)

[result] (tool result omitted)

[assistant] (ran Bash: Test whether llm config env reaches a containerized mock run)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Investigated the state and measured the ground truth. Summary before the decisions: **Where things stand.** J14 landed and is committed. `prior-glare` (J15 container journey) is the open acceptance thread. The blocker it named — mock having no container profile — was fixed in `be43c4c4`. **What I measured on this host (docker 29.3.0 reachable):** - The three hermetic J15 rows are ready to write. `container check <backend>` exits 0 and renders all four axes; `container check`/`container build not-a-backend` both exit 1 naming the bad backend and listing the real set. - A containerized mock run **works and is fast** — ~3s, with `docker events` showing 7 real create/start/die/destroy cycles on `ctxloom-agent:<tag>`. - `ctxloom container build mock` costs **452s cold and 393s on a second run** in this repo (the devcontainer base makes a 15.9GB image). It does not meaningfully cache. The image tag is a hash of the ctxloom binary + companions + base config, so any binary change mints a new tag — in CI that means a cold build every time. - **A defect, filed as `untainted-chemist`:** containerized runs silently drop `llm.configs.<label>.env`. Same fixture, one line changed — `runtime: host` echoes `CTXLOOM_MOCK_RESPONSE` and writes the record file; `runtime: container` echoes the default and writes nothing. Both exit 0, and the container demonstrably launched. That is the documented credential channel, so a containerized run with a provider key in config gets an engine with no credential and no warning. Same class as `wisplike-armhole`, different path. That last one is load-bearing for J15: the mock record file is how every other isolation row proves *where* the engine ran, and it doesn't survive the container boundary today. Without it, a run row could only assert exit 0 — the exact false-green class the project bans.

[result] (tool result omitted)

[assistant] Proceeding: commit the prior unit, then fix `untainted-chemist`, then wire J15 (hermetic rows in the default suite, run row behind its own just target).

[assistant] (ran Bash: Review remaining uncommitted diffs for coherence)

[result] (tool result omitted)

[assistant] (ran Bash: Run internal/cli package tests)

[assistant] (ran Bash: Run grpc and memory package tests)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Show manage.go diff)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the excluded-scenario rot guard unit)

[result] (tool result omitted)

[assistant] (ran Bash: Amend commit with the remaining rot-guard files)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the session identity round-trip unit)

[result] (tool result omitted)

[assistant] (ran Bash: Compare previousSessionByHarp and distillSessionOnce)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Two commits landed. Now to the env defect blocking J15's run row.

[assistant] (ran Bash: Locate llmEnvFor and its callers)

[result] (tool result omitted)

[assistant] (ran Bash: Trace llmEnv usage in run.go)

[result] (tool result omitted)

[assistant] (ran Bash: Read where llmEnv is applied)

[result] (tool result omitted)

[assistant] (ran Bash: Trace runEnv consumers)

[result] (tool result omitted)

[assistant] (ran Bash: Read the run options construction)

[result] (tool result omitted)

[assistant] (ran Bash: Find how the ctxloom binary enters the agent image)

[result] (tool result omitted)

[assistant] (ran Bash: Find env handling in the container spawn path)

[result] (tool result omitted)

[assistant] (ran Bash: Read the container env plumbing)

[result] (tool result omitted)

[assistant] (ran Bash: Decisive test: does the label env reach the containerized engine)

[result] (tool result omitted)

[assistant] (ran Bash: Search for RunOptions.Env handling in the wire layer)

[result] (tool result omitted)

[assistant] (ran Bash: Read server-side env handling)

[result] (tool result omitted)

[assistant] (ran Bash: Read getEnvFromMap)

[result] (tool result omitted)

[assistant] (ran Bash: Read llmEnvFor and SpawnEnv wiring)

[result] (tool result omitted)

[assistant] (ran Bash: Read the run dispatch around container vs host client)

[result] (tool result omitted)

[assistant] (ran Bash: Read startContainerOwnedRun)

[result] (tool result omitted)

[assistant] (ran Bash: Trace OwnerRunSpec.Env in agentcoord)

[result] (tool result omitted)

[assistant] (ran Bash: Read owner_run env forwarding)

[result] (tool result omitted)

[assistant] (ran Bash: Read buildHarnessSpec)

[result] (tool result omitted)

[assistant] (ran Bash: Read the decode side and its consumers)

[result] (tool result omitted)

[assistant] (ran Bash: Read rest of decodeHarnessSpec and ChatRequest.Env use)

[result] (tool result omitted)

[assistant] (ran Bash: Find mock's Chat implementation)

[result] (tool result omitted)

[assistant] Correction to my earlier diagnosis — I traced it to the end and the evidence meant something different than I said.

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Read containerMarkers)

[result] (tool result omitted)

[assistant] Design before I build it — three surfaces change: **1. New leaf package `internal/shared/containerprobe`** (extraction, no new dependency): ```go func Markers() []string // matched in-container markers, empty = none func MarkersFrom(stat func(string) error, // the existing seam, moved verbatim readFile func(string) ([]byte, error), getenv func(string) string) []string func InContainer() bool ``` `internal/lm/isolation` keeps `InContainer()`/`containerMarkers()` as thin wrappers, so no existing call site changes. The extraction exists because `backends` cannot import `isolation` (cycle: isolation → backends → acp → isolation) and duplicating the probe is what reprise is there to stop. **2. `internal/lm/backends/mock.go`** — the record gains two lines and the writer loses its `ExecuteRequest` coupling: ```go type mockRecordFields struct { Mode int32; WorkDir string; Env map[string]string; Context, Prompt string; FragmentCount int } func writeMockRecord(recordFile string, in mockRecordFields, managed *agent.ManagedConfig) error func recordMockInput(...) // unchanged signature, now a thin adapter ``` New record lines: `hostname=` and `container_markers=`. Two independent signals, which matters because the suite can itself run inside a devcontainer — markers alone would then be true on both sides, while the hostname still differs. **3. `internal/lm/backends/mock_chat.go`** — `Chat` honors `CTXLOOM_MOCK_RECORD_FILE`, nothing else. I am deliberately *not* giving it `CTXLOOM_MOCK_RESPONSE`/`EXIT_CODE`: changing the `"mock chat: <text>"` echo would disturb tests that assert it, and recording is all J15 needs.

[assistant] (ran Bash: Find record-format consumers)

[result] (tool result omitted)

[assistant] (ran Bash: Check for exact-body record assertions)

[result] (tool result omitted)

[assistant] (ran Bash: Find all users of the in-container probe)

[result] (tool result omitted)

[assistant] Small, contained surface. Implementing.

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Read runtime.go imports)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Now the mock record writer and `Chat`'s recording.

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Now `Chat` records its turn.

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Build and run backend + probe package tests)

[result] (tool result omitted)

[assistant] (ran Bash: Build and run affected package tests)

[result] (tool result omitted)

[assistant] Now the decisive experiment — this settles whether the label env actually crosses into the container.

[assistant] (ran Bash: Containerized run with the new Chat recording)

[result] (tool result omitted)

[assistant] (ran Bash: Host baseline record for comparison)

[result] (tool result omitted)

[assistant] (ran Bash: Read mock record tests for style)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Run the new probe and mock chat tests)

[result] (tool result omitted)

[assistant] Mutation check — green alone proves nothing here.

[assistant] (ran Bash: Mutation 1: remove Chat recording)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation 1 via task runner)

[result] (tool result omitted)

[assistant] Mutation killed — both record tests went red. Restoring the mutated line.

[result] (tool result omitted)

[assistant] (ran Bash: Mutation 2: drop the hostname line)

[result] (tool result omitted)

[assistant] (ran Bash: Run the unit test suite)

[result] (tool result omitted)

[assistant] (ran Bash: Capture real exit codes for test and lint)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the mock evidence channel and probe extraction)

[result] (tool result omitted)

[assistant] (ran Bash: Close the mock-evidence task)

[result] (tool result omitted)

[assistant] (ran Bash: Read tag exclusion wiring)

[result] (tool result omitted)

[assistant] (ran Bash: Read j9 fixture setup and the env API)

[result] (tool result omitted)

[assistant] (ran Bash: Find the acceptance Env API)

[result] (tool result omitted)

[assistant] (ran Bash: List TestEnvironment methods)

[result] (tool result omitted)

[assistant] (ran Bash: Read project dir accessor and containercell.Select)

[result] (tool result omitted)

[assistant] Now J15 itself.

[result] (tool result omitted)

[assistant] (ran Bash: See how j9 installs its config and parses records)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Find world registration points)

[result] (tool result omitted)

[assistant] (ran Bash: Read world state fields and step registration)

[result] (tool result omitted)

[assistant] (ran Bash: Register J15 state and steps in the world)

[result] (tool result omitted)

[assistant] Now the tag exclusion in its three places, and the dedicated gate.

[assistant] (ran Bash: Read the tag comment block above the default expression)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Read around test-acceptance for the insertion point)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Run the hermetic J15 rows)

[result] (tool result omitted)

[assistant] Hermetic rows green (3 scenarios, 19 steps), `@container` correctly excluded. Now the real gate.

[assistant] (ran Bash: Run the container acceptance gate)

[result] (tool result omitted)

[assistant] Green means nothing yet. Mutation check on the container row: make a requested container silently run on the host.

[assistant] (ran Bash: Locate the runtime axis resolution)

[result] (tool result omitted)

[assistant] (ran Bash: Read the axes construction)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation: force every run onto the host runtime)

[result] (tool result omitted)

[assistant] Mutation killed with the exact failure it exists to catch. Restoring and running the full gates.

[assistant] (ran Bash: Restore check and run the full acceptance suite)

[result] (tool result omitted)

[assistant] (ran Bash: Unit suite and lint)

[result] (tool result omitted)

[assistant] (ran Bash: Commit J15)

[result] (tool result omitted)

[assistant] (ran Bash: Find references to the draft directory)

[result] (tool result omitted)

[assistant] (ran Bash: Read the coverage-gaps passages about the draft)

[result] (tool result omitted)

[assistant] (ran Bash: Read the j14 draft reference)

[result] (tool result omitted)

[assistant] (ran Bash: Retire the draft and correct the coverage-gaps doc)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the draft retirement and doc correction)

[result] (tool result omitted)

[assistant] (ran Bash: Close the J15 tasks and show final state)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Add the memory index pointer)

[result] (tool result omitted)

[assistant] J15 is wired and the acceptance thread is closed out. Five commits on `release/0.7`: | commit | what | |---|---| | `71f10797` | excluded-scenario rot guard + the dead `init.feature` deleted | | `e3fe7770` | session-identity essence key fix + its production-shape scenario | | `6ec37654` | `Mock.Chat` leaves the record `Execute` leaves; probe extracted | | `11ad5cf1` | **J15** — 3 hermetic rows + the real containerized-launch row | | `a780d8a5` | J15 draft retired, gap survey corrected | **Gates, by exit code, against a fresh build:** `just test` 0 · `just lint` 0 · `just test-acceptance` 0 (328 scenarios, 2239 steps) · `just test-acceptance-container` 0 (78s). **Signatures that resulted**, including where they diverged from what I proposed: ```go // internal/shared/containerprobe — as proposed func InContainer() bool func Markers() []string func MarkersFrom(stat func(string) error, readFile func(string) ([]byte, error), getenv func(string) string) []string // internal/lm/backends — as proposed type mockRecordFields struct { Mode int32; WorkDir string; Env map[string]string; Context, Prompt string; FragmentCount int } func writeMockRecord(recordFile string, in mockRecordFields, managed *agent.ManagedConfig) error func (b *Mock) recordChatTurn(req agent.ChatRequest, text string) error // tests/acceptance — not in the proposal; J15's own state and parse type j15Record struct { Hostname, Markers string } func j15ParseRecord(body string) j15Record func j15RecordPath(w *World, runtime string) string ``` Two divergences worth your eye. `isolation.inContainerFrom` survives as a thin delegate rather than being deleted, so the existing table test keeps testing from where it lives. And J15 **reuses J9's `j9ConfigYAML`/`j9HomeConfigYAML` fixture** rather than authoring its own — same two agent bindings, only the record path differs, because a containerized engine can only write where the container can see. Three things I want to flag: **My first diagnosis was wrong and I corrected it mid-task.** The host/container difference looked exactly like containerized runs dropping `llm.configs.<label>.env` — a credential-loss defect, which is how I filed it. Tracing it to the end showed the plumbing intact all the way to `agent.ChatRequest.Env`; `Mock.Chat` simply read none of those knobs. The task was rewritten before any work started on the wrong premise, and the corrected experiment now proves the env *does* reach a containerized engine. **The run row is verified by mutation, not by green.** Forcing the runtime axis to `host` makes it fail with "the engine reported this host's own name, so it did not run in a container". It is differential on purpose — an absolute in-container check would pass vacuously on a harness that itself runs in a devcontainer. **Build cost is the reason the row sits behind its own gate.** `container build` measured 452s cold and 393s on an immediate second run in this repo; the tag hashes the ctxloom binary, so CI has no warm cache to rely on. A containerized run is ~3s once the image exists. Filed: `strained-cathouse` (the `container tooling` trust gate — withheld and nothing-declared print identically, which I did not cover) and `hatless-enclosure` (reprise flagged `previousSessionByHarp` as possibly carrying the same essence-key defect; unverified, marked as a lead). Closed: `prior-glare`, `zesty-slip`, `untainted-chemist`. Still uncommitted, untouched by me: the `.ctxloom`/`.kiro`/`.codex` materialization churn.

[user] ok, continue with acceptance testing

[assistant] (ran Bash: Check the weave/map removal task)

[result] (tool result omitted)

[assistant] (ran Bash: Locate the completeness allowlist)

[result] (tool result omitted)

[assistant] (ran Bash: Read the knownUncovered allowlists)

[result] (tool result omitted)

[assistant] (ran Bash: Read knownUncoveredCLI entries)

[result] (tool result omitted)

[assistant] (ran Bash: Read the live uncovered set)

[result] (tool result omitted)

[assistant] (ran Bash: Read the uncovered set from the full run log)

[result] (tool result omitted)

[assistant] Next target: the three `distill` leaves whose only coverage is `@live` — the ratchet comment names J14 as the worked example to copy. First, a line I got wrong in the last commit.

[assistant] (ran Bash: Re-read the J13 claim I wrote)

[result] (tool result omitted)

[assistant] (ran Bash: Read the live distill feature)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Read J14's mock-LLM distill pattern)

[result] (tool result omitted)

[assistant] (ran Bash: Find the live distill step definitions)

[result] (tool result omitted)

[assistant] (ran Bash: Read the live distill steps in full)

[result] (tool result omitted)

[assistant] (ran Bash: Read the MockLM helper)

[result] (tool result omitted)

[assistant] (ran Bash: Find the doc's recommendation for the distill leaves)

[result] (tool result omitted)

[assistant] (ran Bash: Find the shared mock-LLM step)

[result] (tool result omitted)

[assistant] (ran Bash: Read the mock LLM fixture steps)

[result] (tool result omitted)

[assistant] (ran Bash: Read fragment distill help)

[result] (tool result omitted)

[assistant] (ran Bash: Read the live fixture builders)

[result] (tool result omitted)

[assistant] (ran Bash: Check mock response building and distiller output)

[result] (tool result omitted)

[assistant] (ran Bash: Read the distiller body)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Find the config version constant and the file steps context)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Now the step definitions for the distill scenarios.

[result] (tool result omitted)

[assistant] (ran Bash: Register and run the content distill feature)

[result] (tool result omitted)

[assistant] (ran Bash: Read the per-step results)

[result] (tool result omitted)

[assistant] (ran Bash: Read the actual command output for the degrade scenario)

[result] (tool result omitted)

[assistant] (ran Bash: Manual repro of hermetic fragment distill)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run and read the command output on failure)

[result] (tool result omitted)

[assistant] A real production guard surfaced: distillation rejects a suspiciously short answer.

[assistant] (ran Bash: Locate the truncation guard)

[result] (tool result omitted)

[assistant] (ran Bash: Read the truncation guard rule)

[result] (tool result omitted)

[assistant] (ran Bash: Read distillTooShort)

[result] (tool result omitted)

[assistant] (ran Bash: Test the no-LLM degrade arm by hand)

[result] (tool result omitted)

[assistant] (ran Bash: Read ResolveLLM's fallback)

[result] (tool result omitted)

[assistant] Found it: my canned answer was 31 bytes against a 1755-byte fixture, and `distillTooShort` rejects anything under 2%. That guard is worth a scenario of its own.

[result] (tool result omitted)

[assistant] (ran Bash: Check MockLM exit-code support)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Run the revised distill feature)

[result] (tool result omitted)

[assistant] (ran Bash: Identify the undefined step)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run the distill feature)

[result] (tool result omitted)

[assistant] Scenarios green; the run still exits 1 — that should be the completeness gate reporting the allowlist is now stale, which is exactly the signal I want.

[assistant] (ran Bash: Confirm the completeness gate's complaint)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Correct the allowlist size comment)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run with the pruned allowlist)

[result] (tool result omitted)

[assistant] Mutation checks on the two new claims.

[assistant] (ran Bash: Mutation: disable the truncation guard)

[result] (tool result omitted)

[assistant] The mutation died on the warning assertion first, which means the payload assertion never ran. Reordering so state is asserted before message, then re-running.

[result] (tool result omitted)

[assistant] (ran Bash: Reorder both scenarios and re-run the mutation)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation: discard the distiller's answer)

[result] (tool result omitted)

[assistant] The `_ = put` mutation didn't compile, so it proved nothing. Running a valid one: store the original instead of the answer.

[assistant] (ran Bash: Mutation: store the original instead of the distiller's answer)

[result] (tool result omitted)

[assistant] Four scenarios killed it with the exact intended message; the fifth correctly never reaches that code. Full gates now.

[assistant] (ran Bash: Full acceptance, unit, and lint gates)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the hermetic distill coverage)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Three distill leaves closed, ratchet burned down. | | | |---|---| | commit | `3317c237` | | feature | `tests/acceptance/features/content_distill.feature` — 5 scenarios, 40 steps | | ratchet | **24 → 21** (`maxKnownUncoveredTotal`), allowlist pruned | | gates | `just test` 0 · `just lint` 0 · `just test-acceptance` 0 (**333** scenarios, up from 328) | **What the coverage actually is.** These three leaves were credited as covered for a long time on `distill_live.feature` alone, which is `@live` and never runs — they are the leaves that forced the ratchet up to 24 last week when the corpus reader got honest. Exit code is worthless here: distillation is non-fatal at every failure it knows about, so it warns and exits 0 whether it worked or not. Every scenario reads the stored payload, and asserts both halves — the item's content reaching the distiller, and the distiller's answer landing on disk — because either alone passes while the feature is broken. **A production guard found me rather than the other way round.** My first canned answer was 31 bytes against the 1755-byte fixture fragment, and `distillTooShort` rejected it: under a 2% floor, a stub must not replace a good distillation. It now has a scenario, written as a *replacement* rather than a first distillation — the claim worth making isn't that a stub is refused but that refusing it leaves the author's previous answer intact. A guard that rejected the stub and cleared the field would pass the naive version and still lose content. **One thing worth carrying forward beyond this file:** both new scenarios assert *state before message*. godog stops at the first failing step, so a warning check placed first masks the payload assertion behind it. I hit this while mutation-testing — disabling `distillTooShort` initially showed up only as a missing warning, and the assertion that mattered never ran. Reordered, the same mutation fails with "the rejected answer overwrote it". Mutation results: storing the original in place of the distiller's answer fails four scenarios with "byte-identical to its source"; disabling the truncation guard fails the replacement scenario. The fifth scenario correctly survives both — it never reaches that code. New API surface is small and test-side only: ```go // tests/acceptance/steps_file.go — readBundleFragment now delegates func readBundleCommand(w *World, bundle, command string) (content, distilled string, err error) func readBundleItem(w *World, section, bundle, name string) (content, distilled string, err error) // tests/acceptance/steps_content_distill.go func distillerAnswer(w *World, kind, name, stored, original string) error ``` Filed `bucked-hermit`: `newDistiller`'s "content will be stored RAW (undistilled)" guidance appears to be **unreachable**. `Config.ResolveLLM` returns `DefaultLLM` for a label it can't find, never `""`, so a project with no `llm` section still builds a distiller against the default backend — and the author gets `distill of fragment "rules" failed: LLM exited with code 1:` with an empty string where the backend's stderr should be. The message written for exactly that misconfiguration never fires. I pinned current behavior in the scenario and left the fix to the task; it needs a decision about whether to make the fallback conditional or move the guidance. Also corrected a claim I made in my previous turn: I'd written that J13 was "the last unwritten journey". It isn't pending at all — its draft narrated `ctxloom map`, which no longer existed, and `weave` (the surviving spelling) is itself slated for removal under `hungry-bribe`. The doc now says that. Remaining allowlist, if you want to keep going: 4 `hook` machine callbacks (`weary-crowd`), `acp client`/`acp serve`, `mcp server edit`, `session watch`, the `--engine` matrix rows for codex/kiro/antigravity (`glad-skid`), 4 MCP tools, and 3 runner-only tools.

[user] keep going

[assistant] (ran Bash: Check existing per-engine coverage)

[result] (tool result omitted)

[assistant] (ran Bash: Read the two candidate backfill tasks)

[result] (tool result omitted)

[assistant] Going with the four hidden `hook` leaves (`weary-crowd`) over the engine matrix — the matrix includes antigravity, which `frosty-punk` slates for removal, and the hooks run on every session while being structurally invisible to the gate.

[assistant] (ran Bash: Find the hook commands)

[result] (tool result omitted)

[assistant] (ran Bash: Read inject-context output and session-bind)

[result] (tool result omitted)

[assistant] (ran Bash: Locate the session-bind command)

[result] (tool result omitted)

[assistant] (ran Bash: Read session-bind in full)

[result] (tool result omitted)

[assistant] (ran Bash: Check stdin support in the harness)

[result] (tool result omitted)

[assistant] (ran Bash: Read runCLI and the CLI step vocabulary)

[result] (tool result omitted)

[assistant] (ran Bash: Read stamp-plan)

[result] (tool result omitted)

[assistant] (ran Bash: Read hud)

[result] (tool result omitted)

[assistant] (ran Bash: Read runHookHud body)

[result] (tool result omitted)

[assistant] (ran Bash: Check env steps and LastOutput semantics)

[result] (tool result omitted)

[assistant] (ran Bash: Read the session index fixture helper)

[result] (tool result omitted)

[assistant] (ran Bash: Read the rest of bindSessionFromPayload)

[result] (tool result omitted)

[assistant] (ran Bash: List file steps and read IsPlanFile)

[result] (tool result omitted)

[assistant] (ran Bash: Read the plan-file regex and stamper)

[result] (tool result omitted)

[assistant] (ran Bash: Read formatHud)

[result] (tool result omitted)

[assistant] Now the feature and its steps.

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Register and run the session hooks feature)

[result] (tool result omitted)

[assistant] (ran Bash: Read SetEnv and isolatedEnv)

[result] (tool result omitted)

[assistant] (ran Bash: Read scrubSessionEnv)

[result] (tool result omitted)

[assistant] (ran Bash: Read sessionEnvKeys)

[result] (tool result omitted)

[assistant] (ran Bash: Read the scrubbed env key list)

[result] (tool result omitted)

[assistant] (ran Bash: Confirm whether the harp var is scrubbed)

[result] (tool result omitted)

[assistant] The harp is deliberately scrubbed from every child process — which is precisely why nothing could cover these hooks. The harness needs a seam for that.

[assistant] (ran Bash: Read the TestEnvironment struct fields)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run the hooks feature with the child-env seam)

[result] (tool result omitted)

[assistant] All 7 pass; the exit-1 is the completeness gate again reporting the allowlist has rotted.

[assistant] (ran Bash: Confirm which entries are now covered)

[result] (tool result omitted)

[assistant] (ran Bash: Prune the hook entries and lower the ratchet)

[result] (tool result omitted)

[assistant] Now mutation checks on the new claims.

[assistant] (ran Bash: Mutation: drop the index bind while keeping the marker)

[result] (tool result omitted)

[assistant] The mutation broke the build (unused import), so it proved nothing. Using one that compiles.

[assistant] (ran Bash: Mutation: bind an empty session id)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation: stamp every edited file, not just plans)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation: deliver empty context)

[result] (tool result omitted)

[assistant] (ran Bash: Full gates including integration)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the session-hooks coverage)

[result] (tool result omitted)

[assistant] (ran Bash: Close the task and list what remains uncovered)

[result] (tool result omitted)

[assistant] Four hidden hooks closed. `weary-crowd` done. | | | |---|---| | commit | `08d98a99` | | feature | `tests/acceptance/features/session_hooks.feature` — 7 scenarios, 42 steps | | ratchet | **21 → 17** (24 → 17 across both turns) | | gates | `just test` 0 · `just lint` 0 · `just test-integration` 0 · `just test-acceptance` 0 (**340** scenarios) | **Why these were uncovered turned out to be structural, not neglect.** `CTXLOOM_SESSION_HARP` is on the ambient-session scrub list, so no scenario could hand a child process the harp that `session-bind` and `stamp-plan` do nothing without — the only channel those variables travel on was closed in both directions. That scrub is correct and stays; it's why these tests can't pass by picking up your own harp. So the harness gained one deliberate door: ```go // tests/integration/testenv func (e *TestEnvironment) SetChildEnv(key, value string) ``` The value is one the scenario *chose*, so it can never be the ambient one. It filters the ambient namesake out before appending, because a duplicate key in a child's environment is resolved by the C library and glibc's `getenv` returns the **first** match — appending alone would silently lose to whatever the host had set. **The channel is asserted, not just the payload.** Every delivery assertion parses stdout *alone* as JSON. Stdout is the engine's input: a diagnostic printed there isn't a warning anyone reads, it's corruption of the envelope the engine parses. A test reading combined output would pass while ctxloom spliced a warning line into a session's context. That's also why "the hook writes nothing to stdout" is a positive requirement in the no-harp case — a marker naming an empty harp would be injected into the transcript as fact. Mutations, all killed: emptying the delivered context fails the injection scenario; binding an empty session id fails the index assertion **while the marker assertion still passes**, confirming the two halves of `session-bind` fail independently; removing the `IsPlanFile` check makes `stamp-plan` write frontmatter into an ordinary file and the negative scenario catches it. One earlier attempt (deleting the `BindSession` call outright) broke the build via an unused import — that one proved nothing and I replaced it. Remaining 17, and my read on them: - **6 engine-matrix rows** (`manage install` / `config init` × codex, kiro, antigravity) — `glad-skid`. Worth noting a third of these are for antigravity, which `frosty-punk` slates for removal in 0.7.0; writing them now is likely waste. The codex and kiro rows are the real four. - **4 MCP tools** (`compact_session`, `evaluate_triggers`, `get_previous_session`, `list_sessions`) — `list_sessions` is `bumpy-musky`; `get_previous_session` overlaps the `hatless-enclosure` lead I filed earlier, so covering it may settle that question too. - **3 runner-only tools** (`agent_report`, `roster`, `agent_fetch_artifact`) — `spry-niece`. - **`acp client` / `acp serve`** — `acp serve` is a long-running stdio server needing a live ACP client; j25 records it as unverifiable rather than faking it. - **`mcp server edit`**, **`session watch`** — the latter is a long-lived watcher with no bounded exit in this harness (`cheap-pug`). The MCP-tool group is the best next target: the harness already drives tools through `callTool`, and `get_previous_session` would double as evidence for the open `hatless-enclosure` defect lead.

[user] continue

[assistant] (ran Bash: Read the uncovered MCP tools and their reasons)

[result] (tool result omitted)

[assistant] (ran Bash: Confirm nothing of this session's work is uncommitted)

[result] (tool result omitted)

[assistant] Good night. Stopping here — clean state, nothing of this session's work uncommitted (only the pre-existing `.ctxloom`/`.kiro`/`.codex` materialization churn, untouched). **Seven commits on `release/0.7`**, all gates green at the tip (`just test` 0 · `just lint` 0 · `just test-integration` 0 · `just test-acceptance` 0, **340 scenarios**): - `71f10797` rot guard for excluded scenarios + dead `init.feature` deleted - `e3fe7770` session-identity essence key fix - `6ec37654` mock records evidence on the container transport - `11ad5cf1` **J15** — container axis proven, run row behind `just test-acceptance-container` - `3317c237` **content distill** — 3 leaves, ratchet 24 → 21 - `a780d8a5` J15 draft retired, gap survey corrected - `08d98a99` **session hooks** — 4 hidden leaves, ratchet 21 → 17 Coverage debt went **24 → 17** across the session; `prior-glare`, `zesty-slip`, `untainted-chemist`, and `weary-crowd` closed. **Where to pick up:** the 4 session-memory/trigger MCP tools (`compact_session`, `evaluate_triggers`, `get_previous_session`, `list_sessions`), all tracked under `spry-niece`. I'd read the last two lines of that task before starting — `get_previous_session` would double as the experiment that settles `hatless-enclosure`, the unverified lead that `previousSessionByHarp` carries the same essence-key defect `e3fe7770` fixed in its sibling. Three findings filed tonight and still open: `hatless-enclosure` (that lead), `bucked-hermit` (the "stored RAW (undistilled)" guidance looks unreachable — users get a cryptic spawn failure instead), and `strained-cathouse` (`container tooling`'s trust gate — withheld and nothing-declared print identically).

[user] hmmm

[assistant] Something in there not sitting right? If you're eyeing a specific claim, the two most load-bearing ones from tonight are: the container run row is proven by a *differential* (host leg vs container leg on the same machine), not an absolute in-container check — and the ratchet drop 24 → 17 is real coverage, not allowlist slack, since every entry I removed had a scenario that killed a mutation first. If it's something else — a design call you'd have made differently, or a commit you want unpicked — say which and I'll look at it now or first thing.

[user] you're supposed to have a good night skill

[assistant] Not in my available-skills list — let me check whether it exists on the ctxloom side and just isn't materialized.

[assistant] (ran Bash: List ctxloom skills and commands)

[result] (tool result omitted)

[assistant] (ran Bash: Search for a good-night skill anywhere)

[result] (tool result omitted)

[assistant] Found it — the skill exists but is being **withheld**, which is why it never reached me:

[assistant] (ran Bash: Compare declared and on-disk modes)

[result] (tool result omitted)

[assistant] Root cause: declared `0644`, on disk `0664` — git only tracks the executable bit, so a group-writable umask (002) on checkout produces 0664 and the package is withheld. Fixing the mode and finding it in history:

[assistant] (ran Bash: Fix the mode and re-list skills)

[result] (tool result omitted)

[assistant] (ran Bash: Find the skill in git history)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] Found and loaded. It's a coordinator skill for working an **admitted** queue unattended — and its highest-value step is triaging that queue *while you're still awake*, so let me do pre-flight now.

[assistant] (ran Bash: List the tag vocabulary for a queue tag)

[result] (tool result omitted)

[assistant] (ran Bash: Check for in-flight work, stray processes, and pin the base SHA)

[result] (tool result omitted)

[assistant] (ran Bash: Look for an existing queue tag and the prior wave's convention)

[result] (tool result omitted)

[assistant] Pre-flight done. Baseline is green at the pinned base `08d98a99` (`just test` 0 · `just lint` 0 · `just test-integration` 0 · `just test-acceptance` 0, 340 scenarios). Two things you need to decide before you go, both cheaper now than at 3am: **Another coordinator is live in this repo right now** — session `brown-petty-risk` (opus-4-8) has a running `ctxloom run` here. I'll work each item in its own worktree off the pinned base and merge only on a full green gate, but if it's also committing to `release/0.7` we could collide. There are also 5 existing worktrees plus 2 in `/tmp`, none mine — I won't touch them. And there's no `overnight`/`unattended` tag in the vocabulary, so I can't discover the queue — which the skill says is deliberately your call, not mine.

[result] (tool result omitted)

[assistant] Queue admitted. Starting the run. Report header first, so it exists even if I die early.

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Commit the mode fix)

[result] (tool result omitted)

[assistant] The commit failed — and that's the finding sharpening itself: **git cannot record this fix at all**, since it tracks only the executable bit. My task text claimed an interim fix was committed; that's wrong and needs correcting.

[result] (tool result omitted)

[assistant] Item 0 closed (unfixable in-repo; filed with the decision). Item 1: the four MCP tools. Cutting its worktree and re-reading the task first, since register-style claims go stale in both directions.

[assistant] (ran Bash: Create the item-1 worktree from the integration head)

[result] (tool result omitted)

[assistant] (ran Bash: Find the four tools' registrations)

[result] (tool result omitted)

[assistant] (ran Bash: Locate the tool handlers)

[result] (tool result omitted)

[assistant] (ran Bash: Read the four tools' input shapes)

[result] (tool result omitted)

[assistant] (ran Bash: Read the existing MCP tools feature)

[result] (tool result omitted)

[assistant] (ran Bash: List the MCP step vocabulary)

[result] (tool result omitted)

[assistant] (ran Bash: Read the recorded-session fixture and j23 steps)

[result] (tool result omitted)

[assistant] (ran Bash: Check the MCP client's env construction)

[result] (tool result omitted)

[assistant] (ran Bash: Read the memory tools' inputs)

[result] (tool result omitted)

[assistant] (ran Bash: Read list_sessions result and the triggers handler)

[result] (tool result omitted)

[assistant] Writing the three session-tool scenarios, reusing the existing captured-session fixture rather than a parallel one.

[assistant] (ran Bash: Read the tail of mcp_tools.feature)

[result] (tool result omitted)

[assistant] (ran Bash: Update the feature's honesty preamble)

[result] (tool result omitted)

[assistant] (ran Bash: Append the three session-tool scenarios)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Register and run the MCP tools feature)

[result] (tool result omitted)

[assistant] Eight of nine. Let me see which failed and what it actually returned — one of these is the `hatless-enclosure` lead.

[assistant] (ran Bash: Identify the failing scenario and its result)

[result] (tool result omitted)

[assistant] Two findings. `get_previous_session` **passed** — that refutes the `hatless-enclosure` lead. And `compact_session` exposed something: the caller asked for `seeded-quiet-ember-drift`, the result echoes that id back, but the essence was written to `sessions/**juicy-squat-hertz**/essence.md` — an unrelated harp.

[assistant] (ran Bash: Read the compact_session handler)

[result] (tool result omitted)

[assistant] The handler explicitly keys the essence by `s.self.Harp` — the *caller's* harp, not the harp of the session being compacted, with a comment defending that choice. Filing rather than changing it: where an essence lands is a persistence decision with a design rationale attached.

[result] (tool result omitted)

[assistant] (ran Bash: Rewrite the compact_session scenario to assert the payload honestly)

[result] (tool result omitted)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Re-run the MCP tools feature)

[result] (tool result omitted)

[assistant] (ran Bash: Read how tool results are unwrapped)

[result] (tool result omitted)

[result] (tool result omitted)

[assistant] (ran Bash: Fix the field lookup and re-run)

[result] (tool result omitted)

[assistant] Nine of nine. Pruning the three tools from the allowlist and lowering the ratchet.

[assistant] (ran Bash: Prune the allowlist and re-run)

[result] (tool result omitted)

[assistant] (ran Bash: Locate the summary field for the mutation)

[result] (tool result omitted)

[assistant] (ran Bash: Find the list_sessions summary construction)

[result] (tool result omitted)

[assistant] (ran Bash: Read the list_sessions row construction)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation: list_sessions drops the titles)

[result] (tool result omitted)

[assistant] (ran Bash: Locate saveEssence for the payload mutation)

[result] (tool result omitted)

[assistant] (ran Bash: Mutation: persist an empty essence)

[result] (tool result omitted)

[assistant] Mutation killed by three scenarios including both new payload assertions. Full gates on the integrated result now.

[ABOUT TO DO] Full gate suite on item 1's branch

## Fragments available (27)

### close-the-turn
TAGS: turn, task, status, finding, deferral, report, backlog, tag
PREMISE: You are finishing a turn, or you have surfaced something you are not going to fix right now. For anyone about to leave an issue unresolved, or about to report what happened.

### config-hierarchy
TAGS: config, flag, environment variable, config key, precedence, override, schema
PREMISE: You are adding or changing a flag, an environment variable, or a config key; working out which layer won when a setting resolved unexpectedly; or reading configuration to decide how something is declared. For anyone touching how this family of binaries is configured, however the moment is worded.

### coordination-tools
TAGS: agent_run, agent_send, agent_recv, roster, artifact, child agent, run id, mailbox
PREMISE: You are calling one of the agent coordination tools and need to know its semantics -- what agent_run returns, how agent_recv delivers, how a child addresses its parent, how an artifact is fetched. For anyone who has already decided to delegate and now needs the mechanics. Skip this when the question is WHETHER to delegate or HOW to write the brief.

### delegation
TAGS: file, search, sub-agent, finder, lookup, investigation, context, web page
PREMISE: You are about to read several files, run a broad search, fetch a page, or take on a self-contained investigation yourself; or you are deciding who should do a piece of work. For anyone weighing doing it against handing it to a sub-agent.

### design-review-checkpoints
TAGS: design, signature, API surface, library, dependency, interface, struct, type
PREMISE: You are about to propose or begin an implementation, add or drop a library, or close a turn that produced code. For anyone about to settle an API surface the human has not seen yet.

### documentation
TAGS: markdown, document, plan, README, file, comment, changelog, tracking file
PREMISE: You are about to create a markdown file, a plan, a tracking or meta document, or to write a comment recording what changed or when. For anyone whose next write is documentation rather than code.

### error-constants
TAGS: error, constant, sentinel, assertion, test, message, literal
PREMISE: You are declaring an error value, or writing a test assertion against one. For anyone about to type the same message literal in more than one place.

### fail-loud-launch
TAGS: check, validation, startup, refusal, degraded, broken input, error class
PREMISE: You are writing a check that can find something wrong; deciding what a command should do when its input is broken, missing, or untrusted; or choosing between refusing and carrying on with less. For anyone deciding how loudly a failure should surface.

### general
TAGS: package, layer, module, boundary, dependency direction, cohesion, coupling, connascence
PREMISE: You are placing new code, changing a module boundary, or judging whether a change fits the system. For anyone deciding where something belongs, or what is now allowed to depend on what.

### green-is-not-passing
TAGS: test, suite, green, mutation, assertion, evidence, fresh build, binary, coverage
PREMISE: You have just written or changed a test; you are reading a gate result and deciding what it proves; or you are about to describe something as verified, working, or done. For anyone about to treat a green result as evidence, however that moment arises. More than one fragment may apply here; this one is about whether the evidence is real.

### is-this-a-standard
TAGS: standard, library, protocol, validator, health check, reflection, JWT, OAuth, infrastructure
PREMISE: You are about to build infrastructure -- a protocol, a validator, a health check, an auth flow, a parser, a retry policy. For anyone about to write something a well-known standard, or a library this project already wires in, may already cover.

### isolation-axes
TAGS: agent, runtime, workspace, container, worktree, permissions, credential
PREMISE: You are creating, configuring, or delegating to an agent; choosing a runtime or a workspace for one; or relying on isolation you did not explicitly specify. For anyone whose next action puts work in someone else's hands.

### ltk
TAGS: command, shell, task runner, just, hook, redirect, blocked command, release, tag
PREMISE: A command you ran was blocked, refused, or redirected with a suggested replacement; or you are about to invoke a build, test, lint, or release tool directly rather than through the project's task runner. For anyone deciding how to run a command here.

### planning-and-brainstorming
TAGS: plan, sequence, option, approach, dependency, trade-off, ordering
PREMISE: You are turning a request into a sequence of work, weighing more than one approach, or deciding what must happen before what. For anyone about to commit to a plan.

### preexisting-ownership
TAGS: test, gate, failure, red, flake, regression, blame
PREMISE: A test, gate, or check is failing and you are deciding whose problem it is, or about to attribute it to something other than your own change. For anyone about to write or think 'pre-existing', 'unrelated', 'flaky', or 'not mine'.

### problem-solving
TAGS: workaround, root cause, retry, sleep, timeout, threshold, fallback, disabled check
PREMISE: Something is failing and you are considering a workaround, a retry, a sleep, a threshold change, a disabled check, or a fallback; or you are deciding whether to chase the root cause or patch the symptom. For anyone about to route around a defect rather than fix it.

### prompt-authoring
TAGS: brief, prompt, sub-agent, instructions, output contract, scope, gate, timeout
PREMISE: You are about to write the brief, instructions, or task description a sub-agent will work from, or deciding what a child agent must return. For anyone composing instructions for an agent that will not see this conversation.

### prototype
TAGS: shim, fallback, compatibility, migration, deprecation, version check, legacy, old format
PREMISE: You are about to preserve an old code path, add a shim, fallback, version check, or deprecation; hesitating to break existing callers; or choosing between a compatible fix and a correct one. For anyone weighing what already exists against doing it properly.

### pushback
TAGS: test, request, linter, type hint, objection, requirement, acceptance criteria
PREMISE: You are being asked to skip tests, ship without them, ignore types, or work around a linter, or you hold a substantive objection to what was requested. For anyone deciding whether to comply quietly.

### reductive
TAGS: refactor, duplication, dead code, feature, cleanup, wrapper, abstraction
PREMISE: You are starting a feature, a refactor, a multi-file change, or a fix longer than one line. For anyone about to add code to a place they have not yet examined for duplication or dead weight.

### sequential-thinking-usage
TAGS: plan, decomposition, dependency chain, design decision, steps
PREMISE: You are facing a multi-step plan, a decomposition, a dependency chain, or a design decision with more than one moving part. For anyone whose next answer is not obvious and where a wrong turn is expensive.

### source-organization
TAGS: repository, worktree, directory, path, checkout, branch slug, layout
PREMISE: You are about to choose where a repository or worktree lives on disk, or what to name one. For anyone creating a checkout, a worktree directory, or a branch slug.

### string-flow-control
TAGS: error, message, conditional, branch, switch, retry, comparison, substring, typed error
PREMISE: You are about to write a conditional, a switch, or a retry whose test is a string comparison -- Contains, HasPrefix, a substring, a matched message. For anyone about to make program flow depend on the TEXT of an error or a message.

### turn-gates
TAGS: gate, build, test, lint, suite, acceptance, exit code, command, task runner
PREMISE: You are choosing which build, test, lint, or mutation command to run; interpreting an exit code or a suite's output; or deciding whether the gate you ran actually covers the claim you are making. For anyone deciding which command proves a change is done. This is about WHICH gate and how to read it, not about whether a passing test means anything.

### unchecked-bindings
TAGS: comment, docstring, doc, README, prose, list, count, symbol, file, census, annotation
PREMISE: You are about to write prose that names something specific -- a list, a count, a file, a symbol, a supported set -- in a comment, doc, README, commit message, or config annotation; or you are relying on such a statement someone else wrote. For anyone about to state in words something the code can later contradict silently.

### worktree-isolation
TAGS: worktree, sandbox, cell, artifact, report, publish, agent output, absolute path
PREMISE: You are dispatching an agent that will work in its own worktree or cell; you are that agent and about to write something someone else must read; or you are about to trust a report that work was written, archived, or cleaned up. For anyone whose output has to survive leaving a sandbox.

### worktree-lifecycle
TAGS: worktree, branch, merge, commit, prune, reap, integration branch
PREMISE: You are about to create, merge, remove, or prune a worktree, delete a branch, or judge whether a piece of work is finished. For anyone whose next action changes which worktrees or branches exist.
