package acpagent

import (
	"context"
	"strings"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is ctxloom's OWN command system
// (internal/operations/commands.go's ListCommands/GetCommand — the same
// bundle "commands" surface `ctxloom run --command <name>` and the MCP
// commands resource already read) advertised to the connected editor as ACP's
// available_commands_update, plus the inbound half — recognizing one of those
// commands invoked inside an ordinary session/prompt.
//
// This is entirely separate from the engine-side passthrough: when the
// CONNECTED ENGINE (not ctxloom) emits its own available_commands_update
// (e.g. claude-code-acp advertising its own slash commands), that already
// forwards verbatim through ChatEvent.Raw's curated allowlist —
// internal/acp/mapping.go's rawOnlyEvent (producer, inside the engine-driving
// client half) and internal/acpagent/mapping.go's rawOnlyUpdates (re-emitter,
// right here in the agent role) — see mapEvent's dispatch (ev.Entry == nil →
// rawOnlyUpdates(ev.Raw)). Nothing in THIS file duplicates that: a session can
// legitimately advertise both ctxloom's commands (this file) and the
// engine's own (via the passthrough lane above) — they are not alternatives, and an editor sees
// the union of whatever available_commands_update frames arrive.

// emitAvailableCommands sends the session's ctxloom commands as an
// available_commands_update notification. Called once right after the
// session opens (handleSessionNew/handleSessionLoad): session/new and
// session/load's response schemas have no field for this (unlike
// modes/models), so AvailableCommandsUpdate — "commands are ready or have
// changed" — must ride a session/update notification instead. A session
// with no commands configured is a silent no-op (availableCommandsUpdateWire
// returns nil, and emitUpdate treats nil as nothing to send) — never an
// empty-but-present frame implying zero commands exist by design.
func (s *Server) emitAvailableCommands(sess *session) *jsonrpc.Error {
	return s.emitUpdate(sess, availableCommandsUpdateWire(sess.commands))
}

// availableCommandsUpdateWire renders the session's ctxloom commands as the
// ACP available_commands_update session/update variant (schema
// $defs/AvailableCommandsUpdate) — nil when the session advertises none.
func availableCommandsUpdateWire(commands *SessionCommands) any {
	if commands == nil || len(commands.Available) == 0 {
		return nil
	}
	out := make([]api.AvailableCommand, 0, len(commands.Available))
	for _, c := range commands.Available {
		// Description is a REQUIRED field on the wire (schema: required
		// ["name","description"]) but "" is schema-valid — an empty string
		// is present, not fabricated. A command with no authored
		// `description:` in its bundle YAML advertises "" rather than
		// ctxloom inventing prose for it.
		out = append(out, api.AvailableCommand{Name: c.Name, Description: c.Description})
	}
	return api.SessionUpdate{AvailableCommandsUpdate: &api.SessionAvailableCommandsUpdate{AvailableCommands: out}}
}

// expandCommand recognizes a ctxloom command invocation at the START of a
// prompt's flattened text ("/<name> <rest>") and expands it into the
// command's own resolved content plus the user's trailing input, via
// commands.Resolve (SessionCommands's doc explains why the match must be
// EXACT, never fuzzy/prefix: most "/word ..." prompt text is just an ordinary
// user message that happens to start with a slash — a file path, a fraction —
// and misinterpreting it would silently corrupt what the user actually typed).
//
// Returns (text, false, nil) UNCHANGED when: commands is nil (no command
// system configured for this session), text doesn't start with "/", or the
// leading token doesn't match any advertised command name — the middle
// bool is the caller's signal for whether an expansion actually happened
// (see handlePrompt's call site: string EQUALITY is not
// a safe substitute for this, since a resolved command body could in
// principle collide with its own raw invocation text). Returns a jsonrpc
// error only when the leading token DID match a real command but resolving
// its content failed — that must be loud, never a silent fall-back to
// sending the raw, unexpanded slash text to the engine.
func expandCommand(ctx context.Context, commands *SessionCommands, text string) (string, bool, *jsonrpc.Error) {
	if commands == nil || commands.Resolve == nil || !strings.HasPrefix(text, "/") {
		return text, false, nil
	}
	body := text[1:]
	name := body
	rest := ""
	if idx := strings.IndexAny(body, " \n"); idx >= 0 {
		name = body[:idx]
		rest = strings.TrimLeft(body[idx:], " \n")
	}
	if name == "" {
		return text, false, nil
	}
	expanded, ok, err := commands.Resolve(ctx, name, rest)
	if err != nil {
		return text, false, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "ctxloom acp: expand command /" + name + ": " + err.Error()}
	}
	if !ok {
		return text, false, nil
	}
	// A matched command resolving to an empty string used
	// to be treated as a successful expansion — handlePrompt would then
	// overwrite the turn's text with "" and send the engine a zero-byte
	// message, reporting success. An empty resolution is exactly as much a
	// resolve FAILURE as commands.Resolve returning an error would have been:
	// this command has nothing to say, so send nothing is wrong, not the
	// "expansion" this function's contract promises.
	if expanded == "" {
		return text, false, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "ctxloom acp: command /" + name + " resolved to no content — refusing rather than sending the engine an empty message"}
	}
	return expanded, true, nil
}

// expandedCommandBlocks rebuilds a prompt's structured ContentBlocks after
// expandCommand replaced its flattened text (see
// handlePrompt's call site): every ORIGINAL text-kind block — the raw
// "/<name> ..." invocation promptText flattened FROM — is dropped and
// replaced by ONE new text block carrying the identical expanded string,
// prepended ahead of every other block. Non-text blocks (image, audio,
// resource, resource_link) are preserved UNTOUCHED, in their original
// relative order: expanding a command must never discard an attachment
// riding alongside it. This is what keeps a ContentBlocks consumer
// (internal/acp's CLIENT-role driver) seeing the SAME expanded command a
// Text-only consumer sees, instead of silently forwarding the raw,
// unexpanded slash text through the structured path while only the
// flattened text got the expansion — exactly the kind of divergence a naive
// keep-both merge of structural intake and command expansion would
// otherwise have produced.
func expandedCommandBlocks(blocks []agent.ContentBlock, expanded string) []agent.ContentBlock {
	out := make([]agent.ContentBlock, 0, len(blocks)+1)
	out = append(out, agent.ContentBlock{Kind: "text", Text: expanded})
	for _, b := range blocks {
		if b.Kind == "text" {
			continue
		}
		out = append(out, b)
	}
	return out
}
