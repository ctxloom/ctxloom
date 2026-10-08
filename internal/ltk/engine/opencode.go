package engine

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
	"github.com/ctxloom/ctxloom/internal/engines/opencode"
)

// Opencode adapts the hook wire of opencode's bridge plugin, the plugin
// ctxloom generates into an opencode session (opencode runs no hook command
// of its own): ctxloom's own JSON (kit.NativeHookPayload) on stdin, and
// ctxloom's own answer (kit.NativeHookAnswer) on stdout. A deny is an answer
// that blocks; the bridge throws its reason, which fails the tool call with
// that message for the model to read. Which of opencode's tools is a shell or
// a file edit is opencode's own table (opencode.ToolClass). It is a
// runtime-only adapter: ltk cannot install into opencode, which has no hook
// settings; ctxloom's bridge plugin fires `ltk evaluate`.
type Opencode struct{}

// Name is opencode's one registered name.
func (Opencode) Name() string { return string(opencode.EngineName) }

// opencodeInput is the fields of opencode's tool inputs ltk reads: bash's
// command; edit's and write's filePath; apply_patch's patch text.
type opencodeInput struct {
	Command   string `json:"command"`
	FilePath  string `json:"filePath"`
	PatchText string `json:"patchText"`
}

// Decode reads a pre-tool payload. A shell tool is a command; a file-editing
// tool is a path. A tool opencode does not class as either — or an
// apply_patch whose patch touches other than exactly one file, which a single
// Request cannot carry — is ungated, which evaluate denies rather than
// checking half of it.
func (Opencode) Decode(input []byte) (Request, error) {
	var p kit.NativeHookPayload
	if err := json.Unmarshal(input, &p); err != nil {
		return Request{}, fmt.Errorf("opencode hook payload: %w", err)
	}
	var in opencodeInput
	if len(p.ToolInput) > 0 {
		_ = json.Unmarshal(p.ToolInput, &in) // a shape it cannot read leaves the request empty, hence ungated
	}
	req := Request{ToolName: p.Tool}
	class, _ := opencode.ToolClass(p.Tool)
	switch {
	case class == wire.ToolShell:
		req.Command = in.Command
	case class == wire.ToolFileEdit && in.PatchText != "":
		req.FilePath = patchedFile(in.PatchText)
	case class == wire.ToolFileEdit:
		req.FilePath = in.FilePath
	}
	req.ToolUngated = req.Command == "" && req.FilePath == ""
	return req, nil
}

// patchFileLine is a file header of apply_patch's patch grammar.
var patchFileLine = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+?)\s*$`)

// patchedFile is the one file a patch touches; "" when it touches none or
// several.
func patchedFile(patch string) string {
	m := patchFileLine.FindAllStringSubmatch(patch, -1)
	if len(m) != 1 {
		return ""
	}
	return m[0][1]
}

// Encode renders a denial as the bridge's blocking answer.
func (Opencode) Encode(resp Response) (Output, error) {
	return encodeDecision(resp, func(msg string) ([]byte, error) {
		b, err := json.Marshal(kit.NativeHookAnswer{Block: true, Reason: msg})
		return append(b, '\n'), err
	})
}
