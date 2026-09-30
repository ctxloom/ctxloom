//go:build acceptance

// J002300: "delegation — each child sees only its own context, over a real
// two-way bus" (j002300_cross_engine_delegation.feature). Complements J002100
// (j002100_delegation.feature, steps_j002100_delegation.go), which proved the
// PRIVILEGE half of delegation (MCP servers, permission modes, the
// journaled audit trail) using `agent_run` alone. This file proves the two
// things J002100's own comments name as out of reach from this harness: that a
// child's CONTEXT genuinely differs from a sibling's (asserted on content
// the child itself emits, not a config diff), and that `agent_send`/
// `agent_recv` carry real content between coordinator and child —
// previously exercised only at the unit level
// (internal/core/coord/*_test.go).
//
// j002600 was already taken (steps_j002600_worktree_task_store.go, landed on this
// base — not one of the features-draft/ placeholders j001000-j002400 reserve), so
// this journey is numbered j002300.
//
// Both hermetic observables are produced by the child's OWN runner process —
// the coordinator writes neither: the child's canonical transcript
// (internal/adapters/transcript/record.go's transcript.jsonl, a first-party ctxloom
// artifact of the same durable, disk-backed class j002100_delegation.feature
// established for runs.jsonl) proves requirement 3 (distinct context) and
// the coordinator->child half of requirement 4 (a real agent_send call,
// content verified in the child's own recorded next turn); the coordinator's
// own mailbox, read through agent_recv, proves the child->coordinator half
// through the runner's automatic turn report (runner.EngineHost,
// spoolturnresult.go). The @negative-probe scenario is what makes that
// dependency checkable rather than asserted: withhold the runner and neither
// observable appears.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// j002300AgentSpec is one delegated child's fixture identity: which profile/
// bundle backs it, and the one distinctive marker string its fragment
// carries — the payload a sibling's assembled context must never contain.
type j002300AgentSpec struct {
	Name     string
	Profile  string
	Bundle   string
	Fragment string
	Guidance string // the distinct marker this agent's OWN context carries
}

// j002300State is J002300's fixture state: the configured agents and each spawned
// child's session harp (agent_send/transcript reads need a harp, never an
// agent name).
type j002300State struct {
	specs map[string]*j002300AgentSpec
	harps map[string]string // agent name -> spawned session harp
}

func j002300Of(w *World) *j002300State {
	if w.j002300 == nil {
		w.j002300 = &j002300State{
			specs: map[string]*j002300AgentSpec{},
			harps: map[string]string{},
		}
	}
	return w.j002300
}

// j002300BundleYAML renders one agent's backing bundle: a single fragment whose
// content IS the distinguishing marker — mirrors j000400_multi_engine.feature's
// live-sentinel fixture shape (steps_j000400.go's j000400TeamBundleYAML), one fragment
// per bundle rather than j000400's multi-surface bundle since context is the only
// surface this journey needs.
func j002300BundleYAML(s *j002300AgentSpec) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  %s:\n    content: %q\n", s.Fragment, s.Guidance)
}

// j002300ProfileYAML renders one agent's profile: the one bundle that backs it,
// the same ref shape j002100_delegation.feature's fixture uses
// (steps_j002100_delegation.go's j002100ProfileYAML) — proven to resolve correctly
// there.
func j002300ProfileYAML(s *j002300AgentSpec) string {
	return fmt.Sprintf("bundles:\n  - ctxloom:local@bundles/%s\n", s.Bundle)
}

// j002300HermeticConfigYAML renders config.yaml for the hermetic tier: one mock
// LLM config shared by both children (the hermetic tier's own documented
// scope — see the feature file's header note on what "same engine" means
// here), one agent binding per spec. `workspace: none` runs each child
// against the live checkout instead of a fresh worktree — this journey
// proves CONTEXT isolation and the message bus, not workspace isolation
// (j002200_isolation.feature's own job), and the fixture's own bundle/profile
// files are freshly written, uncommitted state that a worktree spawn would
// otherwise refuse to carry over without an explicit dirty_tree_handler
// (operations.handleDirtyParentTree) — irrelevant noise for what this
// journey asserts, and confirmed empirically: without it, agent_run's async
// launch fails outright ("refusing to auto-commit for delegated agent").
func j002300HermeticConfigYAML(specs ...*j002300AgentSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version: %d\nworkspace: none\nllm:\n  configs:\n    fast:\n      type: mock\n  defaults:\n    primary: fast\n    fast: fast\nagents:\n", config.CurrentConfigVersion)
	for _, s := range specs {
		fmt.Fprintf(&b, "  %s:\n    llm: fast\n    profiles:\n      - %s\n%s", s.Name, s.Profile, permissionsBlock("mock", "bypass"))
	}
	return b.String()
}

// j002300PerEngineAgent is the ONE delegated child the per-engine live outline
// configures, in every row. A fixed name (rather than "<engine>-child") is
// deliberate: the outline reuses this file's existing harp-remembering and
// mailbox steps verbatim, which address a child by agent name, and a single
// name keeps the Gherkin identical across rows so the ONLY thing varying
// between them is the engine under test.
const j002300PerEngineAgent = "delegate"

// j002300PerEngineConfigYAML renders config.yaml for ONE per-engine live row:
// the engine's OWN registry config (live_engine_registry.go's liveAgents[key].
// config — already carrying that engine's backend type and the cheap pinned
// model the whole @live lane shares) with a single agent binding appended.
//
// Appending to the registry's own string, rather than re-declaring the llm
// block here, is what keeps ONE source of truth for "which backend type and
// which model does the live lane drive this engine with". The registry entry
// names its llm config after the registry key and points primary+fast at it,
// so the agent binding just names that same key.
//
// The AXES ARE THE CALLER'S, not this function's. j002300's own per-engine row
// asks for host/none: that row proves the delegation round trip, not workspace
// isolation (j002200_isolation.feature's job). P6's container cell asks for
// container-rootless/worktree, because a round trip that has only ever run
// unisolated cannot answer whether delegation survives the boundary — which is
// the whole question isolation exists to settle.
//
// A WORKTREE CALLER NEEDS BOTH A COMMIT AND dirty_tree_handler: "copy", and
// MEASURED 2026-08-24, either one alone is not enough:
//
//   - the COMMIT gives the repo a HEAD to branch from. TestEnvironment.
//     InitGitRepo only runs `git init` plus user config, and AddGitWorktree
//     documents that `git worktree add -b` needs a valid HEAD, so without a
//     commit the spawn cannot carve a worktree at all.
//   - "copy" covers everything written AFTER that commit. ctxloom materializes
//     its own managed files during session startup — .ctxloom-managed,
//     .ctxloom/project-id, .claude/settings.json, .claude/commands/*.md,
//     .ctxloom/cache/context/* and more — so a fixture that commits and stops
//     is dirty again by the time agent_run runs.
//
// The first live run of the container cell failed exactly there: the child
// never launched, and the refusal listed ctxloom's own files, not the
// fixture's. The default "commit" handler cannot rescue it either — that path
// requires dirty_tree_commit_ack, "a human act only; it cannot be set from
// config.yaml, an environment variable, or any per-call parameter", so no
// automated cell can ever satisfy it. "copy" reproduces the changes as
// uncommitted WIP inside the child's worktree, which is what a fixture wants:
// the child sees the bundle and profile that carry its marker.
//
// runtime rides the AGENT BINDING and workspace rides the top level, matching
// j002200ConfigYAML's mock-container binding — the two axes are independent and
// are written where each one actually lives. runtime is emitted only when it is
// not "host": host is the schema's default, and writing it explicitly would put
// a key in the fixture that the host rows never had.
func j002300PerEngineConfigYAML(a liveAgent, llmKey string, s *j002300AgentSpec, runtime, workspace string) string {
	runtimeLine := runtimeBindingLine(runtime)
	// "copy" is scoped to the worktree axis: on a shared workspace the child
	// runs in the project itself, so there is no second checkout to reproduce
	// into and the key would be inert noise in the host rows' fixture.
	dirtyLine := ""
	if workspace == "worktree" {
		dirtyLine = "dirty_tree_handler: copy\n"
	}
	return a.config + fmt.Sprintf(`workspace: %s
%sagents:
  %s:
    llm: %s
%s    profiles:
      - %s
%s`, workspace, dirtyLine, s.Name, llmKey, runtimeLine, s.Profile, permissionsBlock(a.engine, "bypass"))
}

// j002300WriteAgent writes one agent's bundle + profile files.
func j002300WriteAgent(w *World, s *j002300AgentSpec) error {
	if err := testenv.WriteBundleTree(w.env.ProjectDir, s.Bundle, j002300BundleYAML(s)); err != nil {
		return err
	}
	return w.env.WriteFile(".ctxloom/profiles/"+s.Profile+".yaml", j002300ProfileYAML(s))
}

// --- Canonical transcript reading (hermetic observable) ---------------------
//
// ~/.ctxloom/sessions/<harp>/persist/transcript.jsonl is ctxloom's OWN
// captured conversation record (internal/adapters/transcript/record.go's documented
// schema) — a first-party, durable, disk-backed artifact every structured
// engine (mock included) writes through, not a private format being
// scraped. Decoded locally here (mirroring steps_j002100_delegation.go's
// j002100RunFact/j002100JournalLine — a minimal local shadow of the on-disk shape,
// not an import of the internal/adapters/transcript package) rather than trusting an
// in-process struct.

// j002300TranscriptEntry is one KindEntry line's payload
// (transcript.EntryPayload, field-subset).
type j002300TranscriptEntry struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// j002300TranscriptLine is one transcript.jsonl line's envelope
// (transcript.Record, field-subset): only "kind"/"entry" are needed here.
type j002300TranscriptLine struct {
	Kind  string                  `json:"kind"`
	Entry *j002300TranscriptEntry `json:"entry,omitempty"`
}

// j002300TranscriptPath returns harp's canonical transcript path under this
// scenario's isolated HOME (w.env.HomeDir) — built directly rather than via
// internal/core/paths' resolver, which would read the OUTER test process's own
// ambient HOME, not the isolated one a spawned `ctxloom mcp` subprocess
// actually wrote under (the identical reasoning steps_j002100_delegation.go's
// j002100JournalRaw already documents for runs.jsonl).
func j002300TranscriptPath(w *World, harp string) string {
	return filepath.Join(w.env.HomeDir, ".ctxloom", "sessions", harp, "persist", "transcript.jsonl")
}

// j002300ReadTranscriptEntries parses every KindEntry line's payload from harp's
// transcript, in file (append) order.
// j002300ReadTranscriptEntries parses every `kind:"entry"` line of harp's
// transcript. skipped and firstSkipErr report any line that failed to
// unmarshal (this used to `continue` past those silently, with no
// counter or diagnostic, so a corrupted or schema-drifted transcript read as
// merely "fewer entries" and surfaced as a 10-second timeout in
// j002300TranscriptAssistantCount instead of naming the real parse failure).
func j002300ReadTranscriptEntries(w *World, harp string) (out []j002300TranscriptEntry, skipped int, firstSkipErr error, err error) {
	path := j002300TranscriptPath(w, harp)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("j002300: read transcript %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var l j002300TranscriptLine
		if uerr := json.Unmarshal([]byte(line), &l); uerr != nil {
			skipped++
			if firstSkipErr == nil {
				firstSkipErr = fmt.Errorf("line %q: %w", line, uerr)
			}
			continue
		}
		if l.Kind == "entry" && l.Entry != nil {
			out = append(out, *l.Entry)
		}
	}
	return out, skipped, firstSkipErr, nil
}

// j002300TranscriptAssistantCount waits (bounded) for harp's transcript to carry
// AT LEAST want assistant entries, returning them in order. A mock turn in
// the child's runner completes in well under a second locally, but this box
// runs other agents concurrently — polling tolerates load-induced slack
// without a fixed sleep either racing or over-waiting.
func j002300TranscriptAssistantCount(w *World, harp string, want int) ([]j002300TranscriptEntry, error) {
	deadline := time.Now().Add(10 * time.Second)
	var assistants []j002300TranscriptEntry
	var lastErr error
	var lastSkipped int
	var lastSkipErr error
	for time.Now().Before(deadline) {
		entries, skipped, skipErr, err := j002300ReadTranscriptEntries(w, harp)
		if err != nil {
			lastErr = err
		} else {
			lastSkipped, lastSkipErr = skipped, skipErr
			assistants = assistants[:0]
			for _, e := range entries {
				if e.Type == "assistant" {
					assistants = append(assistants, e)
				}
			}
			if len(assistants) >= want {
				return assistants, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("j002300: transcript for harp %q never appeared after 10s: %w", harp, lastErr)
	}
	if lastSkipped > 0 {
		return nil, fmt.Errorf("j002300: transcript for harp %q carries %d assistant entr(y/ies) after 10s, want >= %d -- and %d transcript line(s) failed to parse (first: %v), so this may be a corrupted/schema-drifted transcript, not a slow child",
			harp, len(assistants), want, lastSkipped, lastSkipErr)
	}
	return nil, fmt.Errorf("j002300: transcript for harp %q carries %d assistant entr(y/ies) after 10s, want >= %d", harp, len(assistants), want)
}

// --- The negative probe: withholding the runner ------------------------------

// j002300WithheldRunnerMarker is the decoy runner's dying words. It exists
// only here and in the decoy the withhold step writes, so its presence in the
// launch failure the coordinator's mailbox carries can mean one thing only:
// the process the coordinator stood up as the child's runner was the decoy,
// and it was the runner's absence — nothing upstream of it — that failed the
// run.
const j002300WithheldRunnerMarker = "J002300-RUNNER-WITHHELD-BY-NEGATIVE-PROBE-7f3a1c"

// j002300WithholdRunner stands this scenario's session owner so that no
// runner can stand for any child it spawns, using only behaviour the product
// already has — no test-only seam is compiled into the binary:
//
//  1. The owner (`ctxloom run`, the process that hosts the coordinator) runs
//     from a COPY of the binary under test, and the copy is unlinked as soon
//     as the owner is standing — by which point its OWN runner has already
//     been self-exec'd from the still-present copy. From then on the
//     coordinator's executable path no longer resolves, so selfexec.Path —
//     the one resolver every self-exec of a child's runner goes through —
//     takes its documented upgrade-in-place fallback: a bare PATH lookup
//     for "ctxloom".
//  2. PATH is led by a directory holding a decoy `ctxloom`: a script that
//     prints j002300WithheldRunnerMarker to stderr and exits non-zero. The
//     spawn therefore starts a process that dies at once, so the coordinator
//     attributes the failure to the runner's death — with its stderr tail —
//     rather than to a missing file or to its own dial-home clock.
//
// Nothing about the fixture, the tool calls or the child's config differs
// from the scenarios this probes; only the child's runner is gone.
func j002300WithholdRunner(w *World) error {
	if w.owner != nil {
		return errors.New("j002300: the session owner is already standing; the runner must be withheld before it stands")
	}
	root := filepath.Join(w.env.Root, "withheld-runner")
	decoyDir := filepath.Join(root, "path")
	if err := os.MkdirAll(decoyDir, 0o755); err != nil {
		return fmt.Errorf("j002300: withhold runner: %w", err)
	}
	decoy := "#!/bin/sh\necho '" + j002300WithheldRunnerMarker + "' >&2\nexit 86\n"
	if err := os.WriteFile(filepath.Join(decoyDir, "ctxloom"), []byte(decoy), 0o755); err != nil {
		return fmt.Errorf("j002300: withhold runner: write decoy: %w", err)
	}
	owner := filepath.Join(root, "ctxloom")
	if err := j002300CopyExecutable(w.env.AppBinary, owner); err != nil {
		return fmt.Errorf("j002300: withhold runner: %w", err)
	}
	if err := w.standSessionOwner(owner, "PATH="+decoyDir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	// The owner is standing, its own runner up: from here the owner's path
	// is gone and every child runner spawn falls through to the decoy.
	if err := os.Remove(owner); err != nil {
		return fmt.Errorf("j002300: withhold runner: unlink the owner's binary: %w", err)
	}
	return nil
}

// j002300CopyExecutable copies src to dst, executable.
func j002300CopyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func registerJ002300Steps(ctx *godog.ScenarioContext) {
	// --- Hermetic fixture -------------------------------------------------

	ctx.Step(`^Alice's coordinator can delegate to two agents, "([^"]*)" and "([^"]*)", each carrying its own distinct guidance in its own profile$`,
		func(c context.Context, nameA, nameB string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			specA := &j002300AgentSpec{
				Name: nameA, Profile: nameA + "-profile", Bundle: "bundle-" + nameA, Fragment: "guidance",
				Guidance: "J002300-GUIDANCE-" + strings.ToUpper(nameA) + "-3f8a91: cite primary sources only, never secondary summaries.",
			}
			specB := &j002300AgentSpec{
				Name: nameB, Profile: nameB + "-profile", Bundle: "bundle-" + nameB, Fragment: "guidance",
				Guidance: "J002300-GUIDANCE-" + strings.ToUpper(nameB) + "-7c2d64: express every distance in metric units only.",
			}
			j002300.specs[nameA] = specA
			j002300.specs[nameB] = specB
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, specA); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, specB); err != nil {
				return err
			}
			return w.env.WriteFile(".ctxloom/config.yaml", j002300HermeticConfigYAML(specA, specB))
		})

	// The per-engine floor's gate (see the feature file's own header for what
	// each row proves). One engine, named by its BACKEND TYPE — the same
	// vocabulary isolation_probe.feature's rows use, resolved onto the
	// registry's own key by backendTypeToLiveKey, so "claude-code" and
	// "claude" can never drift into two different notions of the same engine.
	//
	// Everything the row needs is written HERE, before a single paid turn is
	// spent, and an unavailable engine skips with its own reason printed by
	// name — the identical discipline steps_live.go applies.
	ctx.Step(`^a real "([^"]*)" engine is available for a delegated child carrying marker "([^"]*)"$`,
		func(c context.Context, engine, marker string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			key := backendTypeToLiveKey(engine)
			a, ok := liveAgents[key]
			if !ok {
				return fmt.Errorf("j002300: %q is not a known live engine (registry keys: %v) — a row naming an engine the registry cannot probe would skip forever and look like coverage", engine, liveAgentOrder)
			}
			// EMPTY-MARKER GUARD. The whole row's assertion is
			// strings.Contains(body, marker); an empty marker is contained in
			// every string ever sent, so a blank Examples cell would turn the
			// one payload assertion into a tautology that passes on a
			// runner-exit report — this project's characteristic silent
			// false-green, in its acceptance-suite form.
			if strings.TrimSpace(marker) == "" {
				return fmt.Errorf("j002300: per-engine row for %q carries an EMPTY marker — every body contains the empty string, so this row would pass without the child ever running", engine)
			}
			status := probeEngine(key, a, realHomeDir, resolveOptIn())
			// Loud either way, in the suite's own one-line report shape.
			w.docStepMaterialized = formatLiveEngineReport([]engineStatus{status})
			if !status.available {
				fmt.Printf("SKIP j002300 per-engine live delegation for %q: %s\n", engine, status.reason)
				return godog.ErrSkip
			}

			spec := &j002300AgentSpec{
				Name:     j002300PerEngineAgent,
				Profile:  j002300PerEngineAgent + "-profile",
				Bundle:   "bundle-" + j002300PerEngineAgent,
				Fragment: "marker",
				Guidance: marker,
			}
			j002300.specs[spec.Name] = spec

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, spec); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", j002300PerEngineConfigYAML(a, key, spec, "host", "none")); err != nil {
				return err
			}
			// Subscription path: MAP this engine at its real credential
			// directory (erased-collar), never copy — see seedLiveCredentials.
			return seedLiveCredentials(key, a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv)
		})

	// --- Shared: capture a spawned child's harp -----------------------------

	// --- The negative probe -------------------------------------------------

	ctx.Step(`^the coordinator's runner is withheld$`, func(c context.Context) error {
		return j002300WithholdRunner(worldFrom(c))
	})

	// The mailbox half of the probe, asserted with the SAME selector the
	// positive scenario uses: a result-kind message from this child is exactly
	// what a runner would have written, so one arriving with no runner means
	// something answered for it. What must be there instead is the launch
	// failure, carrying the decoy's own dying words — a failure for any other
	// reason (config, fixture, mint) would not carry them.
	ctx.Step(`^the received message from "([^"]*)" is the withheld runner's launch failure, and no result carrying its guidance arrived$`,
		func(c context.Context, self string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			spec, ok := j002300.specs[self]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", self)
			}
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			if msg, err := j002300FindMessageFrom(w, harp, coord.KindResult); err == nil {
				body, _ := msg[recvTextField].(string)
				return fmt.Errorf("a %q-kind message from %s arrived with no runner standing — something answered for the runner; body:\n%s", coord.KindResult, self, body)
			}
			msg, err := j002300FindMessageFrom(w, harp, coord.KindError)
			if err != nil {
				return err
			}
			body, _ := msg[recvTextField].(string)
			w.docStepMaterialized = fmt.Sprintf("agent_recv — launch failure from %s (harp %s):\n  body: %s", self, harp, body)
			if !strings.Contains(body, j002300WithheldRunnerMarker) {
				return fmt.Errorf("%s's launch failure does not carry the withheld runner's own dying words %q — it failed, but not for the runner's absence; body:\n%s", self, j002300WithheldRunnerMarker, body)
			}
			if strings.Contains(body, spec.Guidance) {
				return fmt.Errorf("the launch failure carries %s's guidance %q, which only a runner-driven turn could have produced; body:\n%s", self, spec.Guidance, body)
			}
			return nil
		})

	// The transcript half: the positive scenarios read the child's own
	// transcript for its turns; with no runner there must be nothing to read.
	// Settled, not racing — the launch failure the previous step read is
	// queued by the run's terminal, after which nothing writes this harp.
	ctx.Step(`^"([^"]*)" recorded no turn$`, func(c context.Context, self string) error {
		w := worldFrom(c)
		j002300 := j002300Of(w)
		harp, ok := j002300.harps[self]
		if !ok {
			return fmt.Errorf("j002300: no session harp remembered for %q", self)
		}
		path := j002300TranscriptPath(w, harp)
		entries, _, _, err := j002300ReadTranscriptEntries(w, harp)
		if errors.Is(err, fs.ErrNotExist) {
			w.docStepMaterialized = path + " — absent"
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type == "assistant" {
				return fmt.Errorf("%s recorded a turn with no runner standing — something answered for the runner; %s carries:\n%s", self, path, e.Content)
			}
		}
		w.docStepMaterialized = fmt.Sprintf("%s — %d entr(y/ies), none an assistant turn", path, len(entries))
		return nil
	})

	ctx.Step(`^"([^"]*)"'s session harp is remembered$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j002300 := j002300Of(w)
		harp, ok := w.lastInner[spawnChildAgentIDField].(string)
		if !ok || harp == "" {
			return fmt.Errorf("j002300: last tool result carries no %s field for %q; result:\n%s", spawnChildAgentIDField, name, w.lastTool.JSON())
		}
		j002300.harps[name] = harp
		return nil
	})

	// --- Hermetic assertions: read the child's OWN canonical transcript ----

	ctx.Step(`^"([^"]*)"'s reported turn carries its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			selfSpec, ok := j002300.specs[self]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", self)
			}
			otherSpec, ok := j002300.specs[other]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", other)
			}
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			assistants, err := j002300TranscriptAssistantCount(w, harp, 1)
			if err != nil {
				return err
			}
			body := assistants[0].Content
			w.docStepMaterialized = fmt.Sprintf("%s — %s's first reported turn:\n  %s", j002300TranscriptPath(w, harp), self, body)
			if !strings.Contains(body, selfSpec.Guidance) {
				return fmt.Errorf("%s's reported turn does not carry its OWN guidance %q; turn:\n%s", self, selfSpec.Guidance, body)
			}
			if strings.Contains(body, otherSpec.Guidance) {
				return fmt.Errorf("CONTEXT LEAK: %s's reported turn unexpectedly carries %s's guidance %q; turn:\n%s", self, other, otherSpec.Guidance, body)
			}
			return nil
		})

	// The agent_send call itself: dynamic (the recipient is a runtime-minted
	// harp, never known ahead of time), so it cannot ride the generic
	// "... with:" table step j002100 uses for agent_run's static args. Still a
	// GENUINE tool invocation through the same callTool the generic step
	// uses (steps_mcp.go) — literally contains `calls tool "agent_send"` so
	// the MCP-tool completeness gate (completeness_test.go's ranAsTool)
	// credits it as real coverage, not vacuous prose.
	ctx.Step(`^the agent calls tool "agent_send" addressed to "([^"]*)"'s session with body "([^"]*)"$`,
		func(c context.Context, name, body string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q — spawn it first", name)
			}
			// kind is REQUIRED on an ordinary send: an absent one is refused,
			// naming the four sender-allowed values. This is plain prose from
			// the coordinator to a child, claiming no special authority.
			return callTool(c, "agent_send", map[string]any{"to_agent_id": harp, "text": body, "kind": pb.MessageKind_MESSAGE_KIND_MESSAGE.String()})
		})

	ctx.Step(`^"([^"]*)"'s next reported turn carries "([^"]*)"$`,
		func(c context.Context, self, want string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			// want >= 2: the SECOND assistant entry is this turn's — the
			// first is the initial "go" turn's, asserted by the guidance
			// step above.
			assistants, err := j002300TranscriptAssistantCount(w, harp, 2)
			if err != nil {
				return err
			}
			body := assistants[len(assistants)-1].Content
			w.docStepMaterialized = fmt.Sprintf("%s — %s's next reported turn:\n  %s", j002300TranscriptPath(w, harp), self, body)
			if !strings.Contains(body, want) {
				return fmt.Errorf("%s's next reported turn does not carry %q; turn:\n%s", self, want, body)
			}
			return nil
		})

	// --- @live-only: agent_recv, retried within a live-turn-sized budget ----
	//
	// testenv.MCPCallTimeout hard-caps EVERY single JSON-RPC round trip
	// this harness makes at 15s, client-side, regardless of the
	// agent_recv tool's own `wait` argument — shared harness code this task
	// must not change. A real engine turn (context load + reasoning +
	// deciding to call agent_send) routinely exceeds that. Retrying
	// agent_recv from the TEST side tolerates it without touching that file:
	// each retry is a free mailbox poll, never a second paid model call, so a
	// generous total budget costs nothing extra. Literally contains `calls
	// tool "agent_recv"` so completeness_test.go's ranAsTool still credits
	// this as real coverage.
	ctx.Step(`^the agent calls tool "agent_recv" repeatedly, waiting up to (\d+)s total, until "([^"]*)" reports$`,
		func(c context.Context, budgetSec int, name string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q — spawn it first", name)
			}
			deadline := time.Now().Add(time.Duration(budgetSec) * time.Second)
			var lastErr error
			for {
				if err := callTool(c, "agent_recv", map[string]any{"wait": 12}); err != nil {
					return fmt.Errorf("j002300: agent_recv transport error while waiting for %q: %w", name, err)
				}
				// A coordinator's receive that times out is a SUCCESSFUL empty
				// result carrying a disposition, not an error — so "until X
				// reports" waits on the payload: a message from X's harp.
				if isErr, msg := w.lastTool.IsError(); isErr {
					lastErr = fmt.Errorf("%s", msg)
				} else if _, ferr := j002300FindMessageFrom(w, harp, ""); ferr == nil {
					return nil // subsequent Then steps assert its content
				} else {
					lastErr = ferr
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("j002300: agent_recv never returned a message from %q within %ds: %w", name, budgetSec, lastErr)
				}
			}
		})

	// --- @live-only: agent_recv, retried until the PAYLOAD arrives ----------
	//
	// The per-engine floor's single assertion, and deliberately not the
	// "wait for any message, then assert on the first one" pair the
	// cross-engine scenario above uses. Two messages reach a coordinator's
	// inbox from the SAME child harp on a live run — the child's own
	// agent_send, and its runner's automatic turn report (coord's
	// spoolturnresult.go) (plus, when an engine fails to authenticate, a runner-exit
	// report). Which one lands in which agent_recv batch is a race, so
	// asserting on "the first message from that harp" would make a genuinely
	// green engine flake red, and — worse for a floor — would let a
	// PASS depend on batch ordering rather than on the payload.
	//
	// So this drains the mailbox until the MARKER BYTES themselves appear,
	// and on expiry reports every body it did see from that child, verbatim:
	// a live failure has to be readable as "the engine said this instead",
	// never as a bare timeout. Each retry is a free local mailbox poll, never
	// a second paid model call. Literally contains `calls tool "agent_recv"`
	// so completeness_test.go's ranAsTool credits it as real coverage.
	//
	// An error-kind message from the child (coord.KindError — a launch
	// failure among them) fails the step AT ONCE with that message's text:
	// the child is dead, the marker can never come, and waiting out the
	// budget only buries the cause under a timeout. The whole batch is
	// scanned for the marker first, so the ordering race above cannot turn
	// a marker that did arrive into a failure.
	ctx.Step(`^the agent calls tool "agent_recv" repeatedly, waiting up to (\d+)s total, until "([^"]*)" reports a body containing "([^"]*)"$`,
		func(c context.Context, budgetSec int, name, want string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q — spawn it first", name)
			}
			if strings.TrimSpace(want) == "" {
				return fmt.Errorf("j002300: waiting for an EMPTY body from %q would be satisfied by any message at all, including a runner-exit report", name)
			}
			errKind, err := pb.MessageKindForLegacyName(coord.KindError)
			if err != nil {
				return fmt.Errorf("j002300: %w", err)
			}
			deadline := time.Now().Add(time.Duration(budgetSec) * time.Second)
			var seen []string
			for {
				done, err := j002300RecvOnce(c, w, name, harp, want, errKind.String(), &seen)
				if err != nil || done {
					return err
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("j002300: %q never sent its coordinator a body containing %q within %ds — %d message(s) arrived from harp %s:\n%s",
						name, want, budgetSec, len(seen), harp, strings.Join(seen, "\n---\n"))
				}
			}
		})

	// --- @live-only assertion: the child's OWN agent_send reply, observed --
	// via agent_recv — the direction the hermetic tier cannot exercise (see
	// this file's header finding). Kept textually distinct from the
	// transcript-based steps above so a reader can never mistake one
	// observable for the other.

	ctx.Step(`^the received message is from "([^"]*)" and its body carries its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			selfSpec, ok := j002300.specs[self]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", self)
			}
			otherSpec, ok := j002300.specs[other]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", other)
			}
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			msg, err := j002300FindMessageFrom(w, harp, coord.KindResult)
			if err != nil {
				return err
			}
			body, _ := msg[recvTextField].(string)
			w.docStepMaterialized = fmt.Sprintf("agent_recv — message from %s (harp %s):\n  body: %s", self, harp, body)
			if !strings.Contains(body, selfSpec.Guidance) {
				return fmt.Errorf("%s's reported body does not carry its OWN guidance %q; body:\n%s", self, selfSpec.Guidance, body)
			}
			if strings.Contains(body, otherSpec.Guidance) {
				return fmt.Errorf("CONTEXT LEAK: %s's reported body unexpectedly carries %s's guidance %q; body:\n%s", self, other, otherSpec.Guidance, body)
			}
			return nil
		})
}

// j002300RecvOnce makes one agent_recv call and scans the batch for messages
// from harp, appending each body to seen. It reports done when a body
// contains want, and fails at once — with that message's text — when harp
// sent an errKind message instead. A tool-level error result is not a
// failure: the caller keeps polling until its budget runs out.
func j002300RecvOnce(c context.Context, w *World, name, harp, want, errKind string, seen *[]string) (bool, error) {
	if err := callTool(c, "agent_recv", map[string]any{"wait": 12}); err != nil {
		return false, fmt.Errorf("j002300: agent_recv transport error while waiting for %q: %w", name, err)
	}
	if isErr, _ := w.lastTool.IsError(); isErr {
		return false, nil
	}
	msgs, merr := j002300Messages(w)
	if merr != nil {
		return false, merr
	}
	var failed []string
	for _, m := range msgs {
		if from, _ := m[recvFromAgentIDField].(string); from != harp {
			continue
		}
		body, _ := m[recvTextField].(string)
		*seen = append(*seen, body)
		if strings.Contains(body, want) {
			w.docStepMaterialized = fmt.Sprintf("agent_recv — message from %s (harp %s):\n  body: %s", name, harp, body)
			return true, nil
		}
		if k, _ := m["kind"].(string); k == errKind {
			failed = append(failed, body)
		}
	}
	if len(failed) > 0 {
		return false, fmt.Errorf("j002300: %q reported a %s message instead of a body containing %q — harp %s:\n%s",
			name, kindLabel(coord.KindError), want, harp, strings.Join(failed, "\n---\n"))
	}
	return false, nil
}

// j002300Messages unwraps an agent_recv result's "messages" array (@live only —
// see the header finding for why the hermetic tier cannot reach this path).
func j002300Messages(w *World) ([]map[string]any, error) {
	raw, ok := w.lastInner["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("j002300: tool result carries no messages array; result:\n%s", w.lastTool.JSON())
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out, nil
}

// j002300FindMessageFrom returns the received message whose sender is harp
// and, when kind (a coord mailbox spelling, e.g. coord.KindResult) is
// non-empty, whose kind is that one's wire name. Pass "" to accept any kind.
//
// The kind filter is not a refinement, it is the correctness condition: one
// harp can have several messages pending, so POSITION selects nothing
// meaningful. Asking for coord.KindResult is how a caller says it wants the
// child's turn result rather than whatever the coordinator queued first.
func j002300FindMessageFrom(w *World, harp, kind string) (map[string]any, error) {
	msgs, err := j002300Messages(w)
	if err != nil {
		return nil, err
	}
	wantKind := ""
	if kind != "" {
		k, err := pb.MessageKindForLegacyName(kind)
		if err != nil {
			return nil, fmt.Errorf("j002300: %w", err)
		}
		wantKind = k.String()
	}
	for _, m := range msgs {
		if f, _ := m[recvFromAgentIDField].(string); f != harp {
			continue
		}
		// One harp can have SEVERAL messages pending, so position is not a
		// selector. More than one message from one child can land in one
		// batch (its own agent_send and its runner's automatic turn report),
		// and taking the first match silently asserted against whichever
		// came first — reporting "the body does not carry its own guidance"
		// for a body that was never the result at all.
		if wantKind != "" {
			if k, _ := m["kind"].(string); k != wantKind {
				continue
			}
		}
		return m, nil
	}
	return nil, fmt.Errorf("j002300: no %s message from harp %q among %d received message(s); result:\n%s",
		kindLabel(kind), harp, len(msgs), w.lastTool.JSON())
}

// kindLabel names the filter in a miss, so "no message from harp X" and "no
// RESULT message from harp X" are distinguishable — they have different causes
// and the second one is the interesting failure.
func kindLabel(kind string) string {
	if kind == "" {
		return "matching"
	}
	return strconv.Quote(kind) + "-kind"
}
