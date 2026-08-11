// This file (oneshot.go) captures the ONE regime the ChatEvent-driven
// Recorder/Tee (recorder.go) structurally cannot reach: a ONESHOT
// Backend.Execute run (`kiro --no-interactive`, `codex exec`)
// returns prose on stdout with no ChatEvent stream at all, so the tee at
// GRPCClient.Chat (internal/lm/grpc/chat.go) and coord/enginehost.startRun
// never fires for it — the structured-capture win of S2/S3 leaves this path
// silently uncaptured (slice S6, "Oneshot Execute").
//
// RecordOneshot writes a deliberately LOW-FIDELITY two-entry canonical
// transcript instead: one `user` entry from the request prompt, one
// `assistant` entry from the captured stdout. No tool granularity (the
// engine's own tool calls inside a oneshot Execute never cross ctxloom's
// process as discrete events), but non-empty memory — closing exactly the
// silent-no-op blind spot memory "silent-no-op-failure-mode" names for the
// kiro-oneshot-sqlite case, without parsing sqlite or any other engine-native
// store.
package transcript

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// RecordOneshot appends a two-entry canonical transcript (user prompt +
// assistant stdout) for one ONESHOT Backend.Execute run, keyed by harp.
//
// Degrades to a silent no-op — the same empty-input discipline NewRecorder
// documents — in two cases: harp is empty (no session to key the file to;
// callers on the interactive-pty path, or a run whose AssignSession failed,
// have no harp and must not synthesize one here), or both prompt and output
// are blank after trimming (nothing to record — this must never write a
// blank transcript that a reader could mistake for "captured, and genuinely
// empty").
//
// A recorder-open or write failure IS returned (unlike Tee/TeeAndClose,
// whose contract is "never perturb the live chat it shadows" because
// capture runs concurrently with it): RecordOneshot is called strictly
// AFTER the oneshot run has already completed, so surfacing the error costs
// nothing at the run's own exit code — it lets the caller (run.go,
// oneshot.go) warn rather than silently losing capture.
func RecordOneshot(harp, engine, prompt, output string) error {
	prompt = strings.TrimSpace(prompt)
	output = strings.TrimSpace(output)
	// Nothing said and nothing produced: legitimately nothing to capture.
	if prompt == "" && output == "" {
		return nil
	}
	// There IS something to capture and no harp to file it under. Returning
	// nil here reported full success for a run whose entire transcript was
	// dropped on the floor — callers only check the error.
	if harp == "" {
		return fmt.Errorf("transcript: oneshot capture: no harp for this run, so %d bytes of prompt and %d bytes of output cannot be recorded", len(prompt), len(output))
	}

	rec, err := NewRecorder(harp, engine)
	if err != nil {
		return fmt.Errorf("transcript: oneshot capture: %w", err)
	}
	defer func() { _ = rec.Close() }()

	if prompt != "" {
		if rerr := rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:    agent.EntryTypeUser,
			Content: prompt,
		}}); rerr != nil {
			return fmt.Errorf("transcript: oneshot capture user entry: %w", rerr)
		}
	}
	if output != "" {
		if rerr := rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:    agent.EntryTypeAssistant,
			Content: output,
		}}); rerr != nil {
			return fmt.Errorf("transcript: oneshot capture assistant entry: %w", rerr)
		}
	}
	return nil
}
