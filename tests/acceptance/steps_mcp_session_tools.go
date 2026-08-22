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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// mcpIndexEntries accumulates this scenario's seeded index rows. The shared
// "a recorded session" fixture in steps_fixture.go writes a SINGLE-entry index
// and would silently drop the earlier harp on a second call — j001300 hit the same
// wall and notes it in j001300WriteIndex. list_sessions is inherently multi-session
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
	// Reading the path the tool REPORTS, rather than one the scenario computes,
	// keeps this step to the one claim it can honestly make: the distillation
	// ran on the seeded transcript. It says nothing about WHERE the essence
	// belongs, and a distillation filed under the wrong session satisfies it
	// perfectly — that claim is the location step below.
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

	// The LOCATION claim, and the one the content step above cannot make on
	// its behalf: an essence with the right content, filed under a session the
	// caller never named, is indistinguishable from a correct run by every
	// field of the result envelope.
	//
	// The path is COMPUTED from the harp the scenario names and the tool is
	// required to have reported that one, rather than the reverse. Compaction's
	// only product is an essence a later load_session/recover_session can find,
	// and those look under ~/.ctxloom/sessions/<harp>/essence.md for the harp
	// being resumed — nowhere else. Filed anywhere else it is not merely
	// mislabelled, it is unreachable.
	ctx.Step(`^the essence the tool reports writing is filed under session "([^"]*)"$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		path, ok := lookupField(w.lastInner, "output_path")
		if !ok || fmt.Sprintf("%v", path) == "" {
			return fmt.Errorf("the tool reported no output_path, so it named no location to verify; result:\n%s", w.lastTool.JSON())
		}
		got := filepath.Clean(fmt.Sprintf("%v", path))
		want := harpEssencePathIn(w, harp)
		if got != want {
			return fmt.Errorf("the essence was filed at %s, but the session the caller named reads its essence from %s — the distillation is attributed to a different session and %s will never find it", got, want, harp)
		}
		if _, err := os.Stat(got); err != nil {
			return fmt.Errorf("the tool reported writing %s but nothing is there (%v)", got, err)
		}
		return nil
	})

	// The contamination half of the same claim, stated separately because the
	// location assertion above cannot see it: writing to the right place and
	// ALSO writing to the caller's is still one session's memory landing under
	// another session's name, and essence.md is overwritten in place — the
	// caller's own distilled context would be gone with no error.
	ctx.Step(`^no essence was written under session "([^"]*)"$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		stray := harpEssencePathIn(w, harp)
		body, err := os.ReadFile(stray)
		if err == nil {
			return fmt.Errorf("an essence was written under %s at %s, which is not the session the caller named — one session's distilled memory filed under another's; essence:\n%s", harp, stray, body)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("could not determine whether %s holds an essence: %v", stray, err)
		}
		return nil
	})
}

// harpEssencePathIn is where this scenario's ctxloom reads and writes a harp's
// distilled essence: <HOME>/.ctxloom/sessions/<harp>/essence.md, mirroring
// paths.HarpEssencePath against the test environment's HOME rather than the
// real one. Computed here rather than imported so the assertion is independent
// of the production path helper it is checking — a helper that started
// returning the wrong directory would otherwise move the goalposts and the
// scenario with them.
func harpEssencePathIn(w *World, harp string) string {
	return filepath.Join(w.env.HomeDir, ".ctxloom", "sessions", harp, "essence.md")
}
