package agent

// Framing for the assembled project context delivered to an engine: claude's
// native --append-system-prompt-file delivery (FrameProjectContext) composes
// from these constants.

// ProjectContextHeader is the heading atop the assembled project context.
const ProjectContextHeader = "# Project Context (assembled by ctxloom)"

// ProjectContextPreamble is the attribution + guidance paragraph shown once,
// ahead of the <ctxloom-context> block. It begins with the blank-line separator
// so it composes directly after the header.
const ProjectContextPreamble = "\n\n_The content below was assembled by ctxloom from your active profile " +
	"(see `.ctxloom/config.yaml` → `defaults.profiles`). It contains the " +
	"coding standards, language conventions, testing practices, and other " +
	"guidance that apply to this project. Treat it as authoritative project " +
	"instructions._" +
	"\n\n_Manage ctxloom with its CLI (run `ctxloom` through your shell): create/edit " +
	"bundles, profiles, fragments, commands, and skills; `ctxloom deps pull`, " +
	"`ctxloom deps upgrade`; `ctxloom manage hooks install`. The ctxloom " +
	"MCP tools are only for retrieving context during the session — searching and loading " +
	"fragments, commands, and prior session history. Task tracking is the " +
	"separate `taskloom` MCP server and `taskloom` CLI._"

// FrameProjectContext wraps assembled context in the single-shot ctxloom
// envelope (header + preamble + <ctxloom-context> block), the whole-file form
// claude loads natively via --append-system-prompt-file. Empty content yields
// "" so an empty context delivers nothing rather than a misleading "content
// loaded" header.
func FrameProjectContext(content string) string {
	if content == "" {
		return ""
	}
	return ProjectContextHeader + ProjectContextPreamble +
		"\n\n<ctxloom-context>\n\n" + content + "\n\n</ctxloom-context>\n"
}
