//go:build acceptance

// J11: plain `ctxloom session distill <harp>` (j11_session_distill.feature).
//
// The stale draft this replaces (features-draft/j14_memory.feature — a
// deleted file, so its ORIGINAL name, not this journey's current number) narrated
// a `ctxloom memory compact/list/show` command group that no longer exists.
// Its list/show claims are already owned by j12_recall.feature; the one live
// claim worth keeping — that distillation genuinely runs the transcript
// through an LLM and persists the result — is proven here against the real
// command instead.
//
// Fixture shape is deliberately borrowed from steps_j12_recall.go
// (j12WriteCanonicalTranscript / j12AddIndexEntry / j12HarpHome): a session
// index entry with session_id "seeded-<harp>" plus a canonical transcript at
// the harp's persist/transcript.jsonl. CanonicalFallbackSource.GetSession
// resolves that id HARP-FIRST and, failing that, reverse-resolves the
// session_id back to its harp via the index (canonical_source.go) — exactly
// the path `session distill` walks in production when a container-bound harp
// has a transcript but no host-side session_id binding. Reusing it here means
// this scenario exercises the same resolution machinery j12 already proves
// works, rather than a second, parallel fixture shape that could quietly
// drift from it.
package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

const (
	// j11Harp is distinct from j12's amber-quiet-heron / brisk-copper-moth so
	// this journey's fixture can never collide with j12's.
	j11Harp = "quiet-ember-forge"

	// j11TranscriptMarker exists ONLY in this journey's seeded transcript, so
	// finding it in the mock's recorded prompt proves the REAL transcript
	// reached the distiller rather than an empty or stale one.
	j11TranscriptMarker = "J11-TRANSCRIPT-REACHED-DISTILLER"
)

func registerJ11SessionDistillSteps(ctx *godog.ScenarioContext) {
	// A dedicated Given rather than reusing steps_fixture.go's shared
	// "the mock LLM responds" step: that step (testenv.MockLM.WriteConfig)
	// only ever writes CTXLOOM_MOCK_* into llm.configs.mock.env in the HOME
	// config.yaml, which reaches the mock backend for `ctxloom run` (whose
	// caller resolves the label's config env via llmEnvFor and forwards it
	// on RunOptions.Env — internal/cli/run.go's st.llmEnv/runEnv) but NOT for
	// this journey's command: internal/memory/compactor.go's runDistill
	// builds its own bare pb.RunOptions{} with no Env field at all, so
	// nothing ever carries the config-declared env to the "ctxloom llm serve
	// mock" subprocess it spawns. Confirmed by hand: pointing only the
	// The mock's knobs are set as PROCESS env, not via the config env.
	//
	// The config-env path (llm.configs.<label>.env) is now forwarded correctly
	// — CompactionConfig.Env -> RunOptions.Env, which was missing entirely —
	// and that forwarding is pinned directly by
	// TestRunDistill_ForwardsConfiguredEnvOntoTheRequest in internal/memory.
	// Routing THIS scenario through the config env as well was attempted and
	// backed out: the mock never received it, which points at acceptance
	// config-LAYER resolution (ensureProjectWithEngine writes a project config,
	// SetupMockLM then patches the same file) rather than at the forwarding.
	// Chasing that here would have made this journey's greenness depend on a
	// config-layering question it does not own, so the env stays on the process
	// channel the other mock-driven journeys already use.
	ctx.Step(`^the mock distiller is configured to respond "([^"]*)"$`, func(c context.Context, response string) error {
		w := worldFrom(c)
		mock, err := w.env.SetupMockLM()
		if err != nil {
			return fmt.Errorf("setup mock LLM: %w", err)
		}
		if err := mock.SetResponse(response); err != nil {
			return fmt.Errorf("set mock response: %w", err)
		}
		w.env.SetEnv("CTXLOOM_MOCK_RECORD_FILE", mock.RecordedInputPath)
		w.env.SetEnv("CTXLOOM_MOCK_RESPONSE", response)
		w.mock = mock
		return nil
	})

	ctx.Step(`^a project whose mock engine is both its primary and its distillation backend$`, func(c context.Context) error {
		// buildJ2Config (steps_j2_common.go) sets llm.defaults.primary AND
		// llm.defaults.fast to the same label, so the mock is the resolved
		// distillation backend (config.Config.FastLabel) without any further
		// setup — matching j12Setup's own "mock, mock" call.
		return ensureProjectWithEngine(worldFrom(c), "mock", "mock")
	})

	ctx.Step(`^an earlier session "([^"]*)" left a real, non-empty transcript on disk$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		transcriptPath := w.env.HomeDir + "/" + j12HarpHome(harp) + "/persist/transcript.jsonl"
		if err := j12AddIndexEntry(w, harp, "seeded acceptance fixture session", transcriptPath); err != nil {
			return fmt.Errorf("seed index entry for %s: %w", harp, err)
		}
		// Two turns, not zero: Compact's isEmptySession short-circuit (zero
		// main-thread entries -> a placeholder dump, no LLM call at all) would
		// make this scenario pass vacuously against an empty session.
		if err := j12WriteCanonicalTranscript(w, harp, []string{
			"What should we do about stale cached responses? " + j11TranscriptMarker,
			"Cache by ETag and revalidate on 304. " + j11TranscriptMarker,
		}); err != nil {
			return fmt.Errorf("seed transcript for %s: %w", harp, err)
		}
		return nil
	})

	// Reads the essence straight off disk (not the command's own "essence:
	// <path>" stdout line) so this proves persistence, not the command's
	// self-report of persistence.
	ctx.Step(`^the persisted essence for "([^"]*)" contains "([^"]*)"$`, func(c context.Context, harp, want string) error {
		w := worldFrom(c)
		body, err := w.env.ReadHomeFile(j12HarpHome(harp) + "/essence.md")
		if err != nil {
			return fmt.Errorf("read essence.md for %s: %w (distill output:\n%s)", harp, err, w.env.LastOutput())
		}
		if !strings.Contains(body, want) {
			return fmt.Errorf("essence.md for %s does not contain %q; on-disk body:\n%s", harp, want, body)
		}
		return nil
	})
}
