package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/resources"
)

// FragmentsResourceURI is the ctxloom:// resource the premise catalog is
// read from; the instructions name it and the MCP servers register it.
const FragmentsResourceURI = "ctxloom://fragments"

// mcpServerInstructions tells the client what this reduced MCP surface is for.
// ctxloom keeps only the agent's runtime context tools here; all management is
// CLI-driven (see cmd/hook_inject_context.go's onload preamble for the same
// guidance injected at session start).
var mcpServerInstructions = resources.MustGetPromptText("mcp-server-instructions")

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
	b.WriteString("\n\n")
	b.WriteString(PremiseSelectionInstruction())
	b.WriteString("\nThe catalog is the `")
	b.WriteString(FragmentsResourceURI)
	b.WriteString("` resource: every conditional fragment,\n")
	b.WriteString("each with its premise and the qualified ref to quote back. Read it when you\n")
	b.WriteString("are ABOUT TO ACT, not once at session start — a premise turns on what you\n")
	b.WriteString("are about to do, so the answer only means something at the moment you have\n")
	b.WriteString("something to match against.\n")
	return b.String()
}

// SessionInstructions renders the MCP server instructions for one caller
// identity (the stdio server's env harp, a runner's launch identity, or a
// coordinator credential's). Both servers serve this ONE text.
func SessionInstructions(harp string) string {
	instructions := mcpServerInstructions + premiseCatalogInstruction()
	if harp == "" {
		return instructions
	}
	// Tell the LLM its own session name so it can self-reference
	// ("save this as the swift-amber-falcon plan") and so plan-
	// stamping correlates the right harp.
	sessionLine := fmt.Sprintf("\n\nYour session is named `%s`. Refer to it by this name when discussing it with the user.", harp)
	// Resume provenance is a property of THIS serving process's session
	// only, so the env read stays gated on the ambient harp matching.
	if resumed := os.Getenv("CTXLOOM_RESUMED_FROM"); resumed != "" && harp == os.Getenv(sessions.EnvHarp) {
		parts := os.Getenv("CTXLOOM_RESUMED_PARTS")
		if parts == "" {
			parts = "session,tasks"
		}
		sessionLine += fmt.Sprintf(" Resumed from `%s` (restored: %s).", resumed, parts)
	}
	// Point the LLM at this session's plan directory. Implementation and
	// strategy plans belong here (not in an ad-hoc .plan/ dir) so they travel
	// with the session and can be recovered on resume. A session may produce
	// several plans, so each is a separately named file with a .plan.md
	// suffix.
	//
	// The path is paths.HarpPlansDir — the harp's persist/ subdirectory — and
	// NOT the harp top level. Only persist/ is bind-mounted into a
	// containerized run (isolation.Container.sessionStateMounts), so an agent
	// that follows this instruction from inside a container and writes at the
	// top level writes into container-ephemeral overlay space and loses the
	// plan on exit: a successful write, a real file, and zero bytes left
	// behind afterwards. This sentence IS the population source for that
	// failure — every session is told where to put its plans right here — so
	// it is the one place the location has to be right.
	if planDir, perr := paths.HarpPlansDir(harp); perr == nil {
		sessionLine += fmt.Sprintf(" Store implementation/strategy plans as markdown files in this session's plan directory `%s`, each named `<descriptive-name>%s` (e.g. `%s`). That directory is the one that survives a containerized run — plans written elsewhere under the session directory do not. A session may have multiple plans — use distinct names and reference plans by their path.", planDir, paths.PlanFileExt, filepath.Join(planDir, "v1-removal"+paths.PlanFileExt))
	}
	return instructions + sessionLine
}
