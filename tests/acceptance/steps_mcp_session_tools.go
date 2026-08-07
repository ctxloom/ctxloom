//go:build acceptance

// Fixtures for the session-memory MCP tools' coverage in mcp_tools.feature
// (list_sessions, compact_session, get_previous_session).
//
// The transcript-bearing fixtures are NOT redefined here: steps_recover_session.go's
// "a captured session ... bound to a backend-native session id" already builds
// exactly the shape these tools need — an index entry whose session_id differs
// from the harp, plus a canonical transcript carrying a unique marker — and
// reusing it means all four session tools are proven against the same fixture
// rather than four fixtures that can drift apart.
//
// Only the multi-session index has no existing home, and that is what this file
// adds.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cucumber/godog"
)

// mcpIndexEntries accumulates this scenario's seeded index rows. The shared
// "a recorded session" fixture in steps_fixture.go writes a SINGLE-entry index
// and would silently drop the earlier harp on a second call — j22 hit the same
// wall and notes it in j22WriteIndex. list_sessions is inherently multi-session
// (a listing that can only be proven with one row proves nothing about
// listing), so it needs the accumulating writer.
type mcpIndexState struct {
	entries []string
}

func mcpIndexOf(w *World) *mcpIndexState {
	if w.mcpIndex == nil {
		w.mcpIndex = &mcpIndexState{}
	}
	return w.mcpIndex
}

func registerMCPSessionToolSteps(ctx *godog.ScenarioContext) {
	// A session in the index with a TITLE, and no transcript: list_sessions
	// reads the index, so this is the whole fixture it needs. The summary is
	// caller-chosen so a scenario can assert the title that belongs to a
	// specific harp rather than that some title came back.
	ctx.Step(`^a recorded session "([^"]*)" summarised as "([^"]*)"$`, func(c context.Context, harp, summary string) error {
		w := worldFrom(c)
		st := mcpIndexOf(w)
		st.entries = append(st.entries, fmt.Sprintf(
			"  - harp_name: %s\n"+
				"    session_id: seeded-%s\n"+
				"    backend: mock\n"+
				"    project_dir: %s\n"+
				"    started_at: 2026-03-14T00:00:00Z\n"+
				"    ended_at: 2026-03-14T02:00:00Z\n"+
				"    summary: %s\n", harp, harp, w.env.ProjectDir, summary))
		return w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", "sessions:\n"+strings.Join(st.entries, ""))
	})

	// Follows the tool's OWN reported output_path and reads what landed there.
	//
	// compact_session's result carries only bookkeeping — chunk count, token
	// counts, a reduction ratio — every field of which a compaction against an
	// empty session fills in just as convincingly. The only way to tell a real
	// distillation from a well-formed report of nothing is to open the file it
	// claims to have written.
	//
	// Reading the path the tool REPORTS rather than one the scenario computes
	// is deliberate: those two disagree today (taskloom onshore-pardon), and
	// this step is meant to prove the distillation ran, not to take a side on
	// where its output belongs.
	ctx.Step(`^the essence the tool reports writing contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		// The UNWRAPPED inner payload, the same one the shared result steps
		// read — lastTool.JSON() is the whole JSON-RPC envelope, where the
		// tool's own fields sit nested behind the transport's.
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		path, ok := lookupField(w.lastInner, "output_path")
		if !ok || fmt.Sprintf("%v", path) == "" {
			return fmt.Errorf("the tool reported no output_path, so it named no artifact to verify; result:\n%s", w.lastTool.JSON())
		}
		outputPath := fmt.Sprintf("%v", path)
		body, err := os.ReadFile(outputPath)
		if err != nil {
			return fmt.Errorf("the tool reported writing %s but it cannot be read (%v) — a success envelope naming an artifact that is not there", outputPath, err)
		}
		if !strings.Contains(string(body), want) {
			return fmt.Errorf("the essence at %s does not carry %q, so the seeded transcript never reached the distiller; essence:\n%s", outputPath, want, body)
		}
		return nil
	})
}
