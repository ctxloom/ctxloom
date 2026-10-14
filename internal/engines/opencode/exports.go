package opencode

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// ExportSchema is the JSON schema of opencode's per-item export block: the
// bytes a bundle item carries under "opencode". One shape for both item
// kinds — a command uses every field, a skill only `enabled`.
var ExportSchema = []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "opencode export block",
  "description": "How a bundle command surfaces as an opencode command, and whether a skill package is offered. Absent means enabled (opt-out).",
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean", "description": "Set to false to withhold the item from opencode. Absent means enabled."},
    "description": {"type": "string", "description": "The command's description in opencode's command list."},
    "model": {"type": "string", "description": "Model override for the command, as provider/model."}
  },
  "additionalProperties": false
}`)

// ExportBlock is opencode's export block, decoded; the zero value is an item
// that declares none: enabled.
type ExportBlock struct {
	Enabled     *bool  `json:"enabled,omitempty"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
}

func (b ExportBlock) isEnabled() bool { return b.Enabled == nil || *b.Enabled }

var decodeExportBlock = kit.ExportDecoder[ExportBlock](EngineName, ExportSchema)

// Exports decodes each item's opencode block: a command is exported unless
// its block opts out (a profile-curated one regardless), described by its
// block or else its authored description; a skill package is offered unless
// its block opts out. The hook events are the bridge plugin's (hookEvents).
func (o Opencode) Exports(items engine.Items) (engine.Exports, error) {
	var out engine.Exports
	for _, item := range items.Commands {
		block, err := decodeExportBlock(item.Exports)
		if err != nil {
			return engine.Exports{}, fmt.Errorf("command %q: %w", item.Ref, err)
		}
		description := block.Description
		if description == "" {
			description = item.Description
		}
		out.Commands = append(out.Commands, engine.CommandExport{
			Name: item.Name, Body: item.Body, Enabled: item.Curated || block.isEnabled(),
			Description: description, Model: block.Model,
		})
	}
	for _, item := range items.Skills {
		block, err := decodeExportBlock(item.Exports)
		if err != nil {
			return engine.Exports{}, fmt.Errorf("skill %q: %w", item.Ref, err)
		}
		out.Skills = append(out.Skills, engine.SkillExport{
			Name: item.Name, Description: item.Description, Files: item.Files,
			Enabled: item.Curated || block.isEnabled(),
		})
	}
	return out, nil
}
