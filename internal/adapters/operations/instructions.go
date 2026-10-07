package operations

import (
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/resources"
)

// FragmentsResourceURI is the ctxloom:// resource the premise catalog is
// read from; the instructions name it and the MCP servers register it.
const FragmentsResourceURI = "ctxloom://fragments"

// InstructionsCharCap is the most server-instruction text a client is known to
// keep. claude truncates past it and keeps the prefix: measured on claude
// 2.1.286, whose --debug log reads "Server instructions truncated from 2659 to
// 2048 chars". The unit is CHARACTERS (claude's own word, and a JS string
// length), so the instructions are measured in runes, not bytes. Re-measure on
// a claude pin bump: `just validate-wake-claude` fails on that log line.
const InstructionsCharCap = 2048

// mcpServerInstructions tells the client what this reduced MCP surface is for.
// ctxloom keeps only the agent's runtime context tools here; all management is
// CLI-driven (agent.ProjectContextPreamble carries the same guidance in the
// session's system prompt).
var mcpServerInstructions = resources.MustGetPromptText("layer-instructions")

// premiseCatalogInstruction tells an MCP client that conditional guidance exists
// and where to ask for it.
//
// The catalog itself is PULLED, never pushed — docs/architecture/core/premise-selection.md
// holds that ruling. What is pushed here is the POINTER, which is the one part a
// client cannot discover on its own: an agent that does not know the catalog
// exists never asks, and the fragments it would have selected are never learned
// to exist.
//
// The selection wording comes from operations.PremiseSelectionInstruction rather
// than a copy. Its three properties were fixed by measurement, and the apparatus
// that measured them was deliberately removed — so a copy that drifts cannot be
// re-derived back to the original. One source, or the measured one loses.
func premiseCatalogInstruction() string {
	var b strings.Builder
	b.WriteString(PremiseSelectionInstruction())
	b.WriteString("\nThe `")
	b.WriteString(FragmentsResourceURI)
	b.WriteString("` resource lists each conditional fragment's premise and the\n")
	b.WriteString("qualified ref to quote back. Read it when you are ABOUT TO ACT.\n")
	return b.String()
}

// SessionInstructions renders the MCP server instructions for one caller
// identity (the stdio server's env harp, a runner's launch identity, or a
// coordinator credential's). Both servers serve this ONE text.
//
// The parts are ordered by what it costs to LOSE them, most costly first,
// because a client may keep only a prefix: claude truncates server
// instructions to its cap (InstructionsCharCap) and silently drops the tail.
// The per-session line leads because nothing else tells the agent its name or
// where its plans survive; the catalog pointer follows because an agent that
// never learns the catalog exists never asks for it; the static surface
// description goes last because it is the same text in every session.
func SessionInstructions(harp string) string {
	parts := []string{premiseCatalogInstruction(), mcpServerInstructions}
	if line := sessionLine(harp); line != "" {
		parts = append([]string{line}, parts...)
	}
	for i, p := range parts {
		parts[i] = strings.TrimRight(p, "\n")
	}
	return strings.Join(parts, "\n\n")
}

// sessionLine is the part of the instructions that names THIS session: its
// harp, its resume provenance, and its plan directory. "" without a harp.
func sessionLine(harp string) string {
	if harp == "" {
		return ""
	}
	// Tell the LLM its own session name so it can self-reference
	// ("save this as the swift-amber-falcon plan") and so plan-
	// stamping correlates the right harp.
	line := fmt.Sprintf("Your session is named `%s`. Refer to it by this name when discussing it with the user.", harp)
	// Resume provenance is a property of THIS serving process's session
	// only, so the env read stays gated on the ambient harp matching.
	if resumed := os.Getenv("CTXLOOM_RESUMED_FROM"); resumed != "" && harp == os.Getenv(sessions.EnvHarp) {
		parts := os.Getenv("CTXLOOM_RESUMED_PARTS")
		if parts == "" {
			parts = "session,tasks"
		}
		line += fmt.Sprintf(" Resumed from `%s` (restored: %s).", resumed, parts)
	}
	// Point the LLM at this session's plan directory. Implementation and
	// strategy plans belong here (not in an ad-hoc .plan/ dir) so they travel
	// with the session, sit where the human reads them, and can be recovered
	// on resume. A session may produce several plans, so each is a separately
	// named file with a .plan.md suffix.
	//
	// The path is the session's OUTPUT dir as THIS process reaches it
	// (sessions.OutputDirIn): this text is rendered by the runner and the
	// agent's own MCP server, which run inside the container for a
	// containerized run, where the output dir is mounted at a different path
	// than the host records. This sentence is the one place every session is
	// told where its plans go, so it is the one place the location has to be
	// right.
	if planDir, perr := sessions.OutputDirIn(harp, os.Getenv); perr == nil {
		// The directory is named ONCE: it is the longest thing in the
		// instructions, and the client truncates them (InstructionsCharCap).
		line += fmt.Sprintf(" Store implementation/strategy plans as markdown files directly in this session's plan directory `%s`, each named `<descriptive-name>%s` (e.g. `v1-removal%s`). Plans written elsewhere do not survive a containerized run. Use a distinct name per plan.", planDir, paths.PlanFileExt, paths.PlanFileExt)
	}
	return line
}
