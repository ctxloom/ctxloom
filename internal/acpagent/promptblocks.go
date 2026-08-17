package acpagent

// This file is the INBOUND projection layer: everything that turns a client's
// session/prompt and session/new payloads into the shapes ctxloom hands the
// engine. It is the mirror of mapping.go, which projects the other direction
// (the engine's events onto ACP session/update frames).
//
// It lives apart from server.go because none of it is the SERVER: these are
// pure functions over wire values, with no Server receiver, no session, no
// connection and no ordering rules. server.go is the protocol state machine —
// the handshake, the session registry, the turn lifecycle — and reading it
// should not mean scrolling through content-block flattening.
//
// Two projections of the same prompt are produced deliberately and must stay
// in step: promptText/promptParts flattens to text for a backend that only
// consumes text, and contentBlocksFromACP carries every block's full bytes
// structurally for one that can act on them. Where a block has no text
// projection the flattened form emits a visible placeholder rather than
// nothing — a silent drop is this codebase's signature failure mode.

import (
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// promptText flattens a prompt's content blocks to text for a backend that
// only ever consumes text (every native backend but internal/acp's — claude/
// codex/kiro/opencode's own StructuredChat drivers, untouched by this slice).
// Text blocks pass through; `resource` blocks inline their embedded
// resource's text; `resource_link` blocks become a labeled reference line —
// so "add context" content reaches the engine instead of vanishing.
//
// image/audio blocks, and a `resource` block carrying only a
// binary blob (no text), have no text projection — but they are no longer
// SILENTLY dropped here: each renders a labeled placeholder line (kind, mime
// type, byte count) so a text-only backend's model at least sees that
// something arrived, instead of the turn quietly missing it (this
// codebase's signature failure mode — exit 0, success, zero bytes
// delivered). A richer backend gets the REAL bytes via
// agent.ChatMessage.ContentBlocks (contentBlocksFromACP), not this flattened
// projection — see handlePrompt.
//
// ContentBlock carries no discriminator field in the fork's generated shape —
// dispatch switches on which variant pointer is non-nil.
func promptText(blocks []api.ContentBlock) string {
	return strings.Join(promptParts(blocks), "\n")
}

// promptParts is promptText's projection before it is joined: one entry per
// block that has any text projection at all, in the prompt's own order.
//
// The split exists because the LEADING entry is the only one a command
// invocation can live in, and a command's arguments must be text the USER
// typed — never one of the media placeholder lines this function interleaves
// into the same flattened string. See handlePrompt's expandCommand call.
func promptParts(blocks []api.ContentBlock) []string {
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			if b.Text.Text != "" {
				parts = append(parts, b.Text.Text)
			}
		case b.Resource != nil:
			if s := embeddedResourceText(b.Resource); s != "" {
				parts = append(parts, s)
			} else {
				parts = append(parts, binaryResourcePlaceholder(b.Resource))
			}
		case b.ResourceLink != nil:
			if s := resourceLinkText(b.ResourceLink); s != "" {
				parts = append(parts, s)
			}
		case b.Image != nil:
			parts = append(parts, mediaPlaceholderLine("image", b.Image.MimeType, len(b.Image.Data)))
		case b.Audio != nil:
			parts = append(parts, mediaPlaceholderLine("audio", b.Audio.MimeType, len(b.Audio.Data)))
		}
	}
	return parts
}

// mediaPlaceholderLine renders the visible, non-silent placeholder for an
// image/audio block flattened to plain text (see promptText's doc comment).
func mediaPlaceholderLine(kind, mimeType string, dataLen int) string {
	return fmt.Sprintf("[%s content received (mimeType=%s, %d bytes) — this flattened text channel cannot render it; a structured-content-aware backend receives it via ContentBlocks instead]", kind, mimeType, dataLen)
}

// binaryResourcePlaceholder renders the visible placeholder for an embedded
// `resource` block carrying only a binary blob (embeddedResourceText returns
// "" for one) — the fix for the pre-existing silent drop this path used to
// have: a blob resource used to vanish with no trace at all.
func binaryResourcePlaceholder(r *api.ContentBlockResource) string {
	if r == nil || r.Resource.BlobResourceContents == nil {
		return ""
	}
	b := r.Resource.BlobResourceContents
	mimeType := ""
	if b.MimeType != nil {
		mimeType = *b.MimeType
	}
	return fmt.Sprintf("[binary resource %s received (mimeType=%s, %d bytes) — this flattened text channel cannot render it; a structured-content-aware backend receives it via ContentBlocks instead]", b.Uri, mimeType, len(b.Blob))
}

// embeddedResourceText inlines an embedded `resource` block's text. ACP embeds
// either a text resource ({uri,text,mimeType}) or a binary blob
// ({uri,blob,mimeType}); only text is inlinable, so a blob yields "" (dropped).
// A uri, when present, prefixes the text as a label. The embedded resource is
// now a PROPERLY TYPED union (TextResourceContents/BlobResourceContents) — the
// pinned SDK left this interface{} (decoded as a raw map), requiring a type
// assertion this function used to do.
func embeddedResourceText(r *api.ContentBlockResource) string {
	if r == nil || r.Resource.TextResourceContents == nil {
		return ""
	}
	t := r.Resource.TextResourceContents
	if t.Text == "" {
		return ""
	}
	if t.Uri != "" {
		return t.Uri + ":\n" + t.Text
	}
	return t.Text
}

// resourceLinkText renders a `resource_link` block as one labeled reference
// line, so a referenced resource reaches the engine as a pointer it can act on
// rather than being dropped. A link with no uri has nothing to reference.
// Title/Description are now PROPERLY TYPED as *string (see
// the pinned SDK's unions_generated.go ContentBlockResourceLink) rather than the
// interface{} the pinned SDK's union file left them as.
func resourceLinkText(l *api.ContentBlockResourceLink) string {
	if l == nil || l.Uri == "" {
		return ""
	}
	label := l.Name
	if l.Title != nil && *l.Title != "" {
		label = *l.Title
	}
	line := "[resource: "
	if label != "" {
		line += label + " — "
	}
	line += l.Uri + "]"
	if l.Description != nil && *l.Description != "" {
		line += " " + *l.Description
	}
	return line
}

// contentBlocksFromACP projects a session/prompt's content blocks onto the
// IR's structured form (agent.ContentBlock: Kind/Text/Raw) — the AGENT-role
// mirror of internal/acp/mapping.go's blockToIR (CLIENT-role intake from an
// ENGINE's own updates). Every variant's FULL bytes ride in Raw regardless of
// kind, so image/audio/resource are carried losslessly all the way to a
// backend that can act on them — this is what makes
// handleInitialize's promptCapabilities.image/audio: true honest: this layer
// never drops the bytes, whatever a specific downstream engine later decides
// to do with them (internal/acp/session.go's buildPromptBlocks degrades that
// per-session, never silently).
func contentBlocksFromACP(blocks []api.ContentBlock) []agent.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]agent.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		kind, text := "", ""
		switch {
		case b.Text != nil:
			kind, text = "text", b.Text.Text
		case b.Image != nil:
			kind = "image"
		case b.Audio != nil:
			kind = "audio"
		case b.ResourceLink != nil:
			kind = "resource_link"
		case b.Resource != nil:
			kind = "resource"
		default:
			continue // no recognized variant — nothing to carry
		}
		raw, err := json.Marshal(b)
		if err != nil {
			// Not expected on a block decoded off the wire, but dropping one
			// in silence is the failure mode this whole function exists to
			// prevent — so the drop is reported rather than inferred later
			// from a turn that mysteriously lost an attachment.
			clidiag.Warn("ctxloom", "acp agent: session/prompt: dropping a %q content block from the structured payload — it could not be re-encoded: %v", kind, err)
			continue
		}
		out = append(out, agent.ContentBlock{Kind: kind, Text: text, Raw: raw})
	}
	return out
}

// mcpServersFromACP maps the client's session mcpServers onto the engine chat
// request shape (env/header list → map). Stdio is the protocol's
// unconditional baseline, always accepted. Http/Sse are now
// accepted too — ctxloom's own initialize advertises both true (see
// handleInitialize) — and carried onward as agent.ChatMCPServer with
// Transport/URL/Headers set; whether the SESSION'S chosen engine can
// actually use them is a separate, per-session question answered by
// internal/acp/session.go's mcpServersToACP (which gates on that engine's
// own advertised capability and reports a loud status when it can't).
//
// The ACP-transport variant (m.Acp, still UNSTABLE in the spec) names an
// ACP-side component ctxloom has no seam to reach yet — accepting it would
// be a lie (it would never actually connect), so it is REJECTED loudly
// rather than silently dropped, same as an entry with no variant set at all
// (a malformed frame a conforming client should never send).
func mcpServersFromACP(servers []api.McpServer) []agent.ChatMCPServer {
	out := make([]agent.ChatMCPServer, 0, len(servers))
	for _, m := range servers {
		switch {
		case m.Stdio != nil:
			var env map[string]string
			if len(m.Stdio.Env) > 0 {
				env = make(map[string]string, len(m.Stdio.Env))
				for _, e := range m.Stdio.Env {
					env[e.Name] = e.Value
				}
			}
			out = append(out, agent.ChatMCPServer{Name: m.Stdio.Name, Command: m.Stdio.Command, Args: m.Stdio.Args, Env: env})
		case m.Http != nil:
			out = append(out, agent.ChatMCPServer{
				Name: m.Http.Name, Transport: agent.MCPTransportHTTP, URL: m.Http.Url,
				Headers: httpHeadersToMap(m.Http.Headers),
			})
		case m.Sse != nil:
			out = append(out, agent.ChatMCPServer{
				Name: m.Sse.Name, Transport: agent.MCPTransportSSE, URL: m.Sse.Url,
				Headers: httpHeadersToMap(m.Sse.Headers),
			})
		case m.Acp != nil:
			clidiag.Warn("ctxloom", "acp agent: session/new mcpServers: %q is an ACP-transport server (McpServer::Acp) — ctxloom has no seam to reach an ACP-side MCP component yet; dropping it rather than forwarding a server that would never connect", m.Acp.Name)
		default:
			clidiag.Warn("ctxloom", "acp agent: session/new mcpServers: an entry set no known transport variant (stdio/http/sse/acp); dropping it")
		}
	}
	return out
}

// httpHeadersToMap converts the ACP wire's HTTP header list to the map shape
// agent.ChatMCPServer.Headers carries. nil for an empty list.
func httpHeadersToMap(headers []api.HttpHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		out[h.Name] = h.Value
	}
	return out
}
