//go:build acceptance

// Turn-transcript fixtures for the hooks that read the transcript of the turn
// now ending (post_tool_hooks.feature): a seeded session record naming the
// engine that wrote the transcript, and that engine's own transcript format.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// vendorTranscriptLines renders one user prompt followed by one assistant
// message in the named engine's transcript format, so the hook's engine-routed
// reader has a genuine vendor file to read rather than a canonical one.
//
// Hand-rendered rather than produced through the reader packages' own types
// so a format change surfaces as a deliberate fixture update instead of the
// fixture silently reshaping itself to match. The claude-code lines mirror the
// minimal shape internal/adapters/cli's hook tests use; the mock line is the whole of
// the mock adapter's format.
func vendorTranscriptLines(engine, closing string) (string, error) {
	text, err := json.Marshal(closing)
	if err != nil {
		return "", err
	}
	switch engine {
	case "claude-code":
		user := `{"type":"user","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"role":"user","content":[{"type":"text","text":"go"}]},"uuid":"u1","timestamp":"2026-08-22T10:00:00.000Z"}`
		assistant := `{"type":"assistant","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":` + string(text) +
			`}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"a1","timestamp":"2026-08-22T10:00:02.000Z"}`
		return user + "\n" + assistant + "\n", nil
	case string(mock.Name):
		return `{"role":"user","text":"go","ts":"2026-08-22T10:00:00Z"}` + "\n" +
			`{"role":"assistant","text":` + string(text) + `,"ts":"2026-08-22T10:00:02Z"}` + "\n", nil
	default:
		return "", fmt.Errorf("no vendor transcript fixture for engine %q", engine)
	}
}

// writeIndexEntry seeds one session for harp on engine by writing its
// per-harp sidecar. The session store is the set of session directories plus
// their session.yaml (the global index.yaml is gone), so seeding the sidecar
// is what makes the session exist -- and it means a scenario can REWRITE the
// seeded state after a command has run, which an index.yaml rewrite cannot do
// once the store has migrated and retired it. withVersion seeds the pinned
// engine version the way SessionStart records it; without it the entry is the
// never-recorded state the hook must refuse to read with a guessed adapter.
func writeIndexEntry(w *World, harp, engine string, withVersion bool) error {
	version := ""
	if withVersion {
		version = j001000SeededEngineVersion(engine)
		if version == "" {
			return fmt.Errorf("no pinned engine version for %q: the fixture would seed a refusal while claiming to seed a readable session", engine)
		}
	}
	return writeSessionSidecar(w, harp, engine, version)
}

// writeSessionSidecar records harp on engine, pinned to engineVersion when
// one is given.
func writeSessionSidecar(w *World, harp, engine, engineVersion string) error {
	return seedSessionSidecar(w, harp, sessionSeed{
		SessionID:     "seeded-" + harp,
		Backend:       engine,
		StartedAt:     "2026-03-14T00:00:00Z",
		EngineVersion: engineVersion,
	})
}

func registerTurnTranscriptSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the session index records harp "([^"]*)" on engine "([^"]*)"$`, func(c context.Context, harp, engine string) error {
		return writeIndexEntry(worldFrom(c), harp, engine, true)
	})

	ctx.Step(`^the session index records harp "([^"]*)" on engine "([^"]*)" with no engine version$`, func(c context.Context, harp, engine string) error {
		return writeIndexEntry(worldFrom(c), harp, engine, false)
	})

	ctx.Step(`^a "([^"]*)" transcript at "([^"]*)" whose turn ends with:$`, func(c context.Context, engine, rel string, doc *godog.DocString) error {
		body, err := vendorTranscriptLines(engine, doc.Content)
		if err != nil {
			return err
		}
		return worldFrom(c).env.WriteFile(rel, body)
	})
}
