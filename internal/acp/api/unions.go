package api

import (
	"encoding/json"
	"fmt"
)

// This file carries ACP's discriminated unions: ContentBlock, SessionUpdate,
// and ToolCallContent. Each follows the same shape — a Type discriminator
// plus one pointer field per variant — and the same Marshal/Unmarshal
// pattern: marshal embeds the active variant's fields alongside the
// discriminator; unmarshal reads the discriminator first, then decodes the
// matching variant. An unrecognized discriminator is a decode ERROR (not a
// silent zero value) — callers are expected to warn-and-drop on it, never to
// crash the stream; see internal/acp/mapping_test.go's
// "unknown sessionUpdate variant fails to decode" for the behavior this
// preserves from the pinned SDK.

// --- ContentBlock ---

// ContentBlockType is ContentBlock's discriminator (schema
// $defs/ContentBlock's "type").
type ContentBlockType string

const (
	ContentBlockTypeText         ContentBlockType = "text"
	ContentBlockTypeImage        ContentBlockType = "image"
	ContentBlockTypeAudio        ContentBlockType = "audio"
	ContentBlockTypeResourceLink ContentBlockType = "resource_link"
	ContentBlockTypeResource     ContentBlockType = "resource"
)

// ContentBlock is a displayable content item (schema $defs/ContentBlock):
// text, image, audio, a resource link, or an embedded resource.
type ContentBlock struct {
	Type ContentBlockType `json:"type"`

	Text         *ContentBlockText
	Image        *ContentBlockImage
	Audio        *ContentBlockAudio
	ResourceLink *ContentBlockResourceLink
	Resource     *ContentBlockResource
}

// ContentBlockText is the text variant's payload (schema $defs/TextContent).
type ContentBlockText struct {
	Text        string      `json:"text"`
	Annotations interface{} `json:"annotations,omitempty"`
}

// ContentBlockImage is the image variant's payload (schema
// $defs/ImageContent).
type ContentBlockImage struct {
	Data        string      `json:"data"`
	MimeType    string      `json:"mimeType"`
	Uri         *string     `json:"uri,omitempty"`
	Annotations interface{} `json:"annotations,omitempty"`
}

// ContentBlockAudio is the audio variant's payload (schema
// $defs/AudioContent).
type ContentBlockAudio struct {
	Data        string      `json:"data"`
	MimeType    string      `json:"mimeType"`
	Annotations interface{} `json:"annotations,omitempty"`
}

// ContentBlockResourceLink is the resource_link variant's payload (schema
// $defs/ResourceLink). Description/MimeType/Size/Title are now PROPERLY
// TYPED (the pinned SDK left them interface{} in its union file — see
// api/unions_generated.go's ContentBlockResourceLink in the old module —
// requiring a type assertion at every read site); internal/acpagent/server.go's
// resourceLinkText reads Title/Description directly now.
type ContentBlockResourceLink struct {
	Name        string      `json:"name"`
	Uri         string      `json:"uri"`
	Description *string     `json:"description,omitempty"`
	MimeType    *string     `json:"mimeType,omitempty"`
	Size        *int64      `json:"size,omitempty"`
	Title       *string     `json:"title,omitempty"`
	Annotations interface{} `json:"annotations,omitempty"`
}

// EmbeddedResourceResource is an embedded resource's payload: either
// TextResourceContents or BlobResourceContents (schema
// $defs/EmbeddedResourceResource), distinguished only by which fields are
// present (no discriminator) — kept as interface{} (decoded as
// map[string]interface{}), matching the pinned SDK; server.go's
// embeddedResourceText reads it as a raw map for exactly that reason.
type EmbeddedResourceResource = interface{}

// ContentBlockResource is the resource variant's payload (schema
// $defs/EmbeddedResource).
type ContentBlockResource struct {
	Resource    *EmbeddedResourceResource `json:"resource,omitempty"`
	Annotations interface{}               `json:"annotations,omitempty"`
}

// MarshalJSON implements the ContentBlock discriminated union.
func (u ContentBlock) MarshalJSON() ([]byte, error) {
	switch u.Type {
	case ContentBlockTypeText:
		if u.Text == nil {
			return nil, fmt.Errorf("acp: ContentBlock text field is required for type text")
		}
		return json.Marshal(struct {
			Type ContentBlockType `json:"type"`
			*ContentBlockText
		}{u.Type, u.Text})
	case ContentBlockTypeImage:
		if u.Image == nil {
			return nil, fmt.Errorf("acp: ContentBlock image field is required for type image")
		}
		return json.Marshal(struct {
			Type ContentBlockType `json:"type"`
			*ContentBlockImage
		}{u.Type, u.Image})
	case ContentBlockTypeAudio:
		if u.Audio == nil {
			return nil, fmt.Errorf("acp: ContentBlock audio field is required for type audio")
		}
		return json.Marshal(struct {
			Type ContentBlockType `json:"type"`
			*ContentBlockAudio
		}{u.Type, u.Audio})
	case ContentBlockTypeResourceLink:
		if u.ResourceLink == nil {
			return nil, fmt.Errorf("acp: ContentBlock resourceLink field is required for type resource_link")
		}
		return json.Marshal(struct {
			Type ContentBlockType `json:"type"`
			*ContentBlockResourceLink
		}{u.Type, u.ResourceLink})
	case ContentBlockTypeResource:
		if u.Resource == nil {
			return nil, fmt.Errorf("acp: ContentBlock resource field is required for type resource")
		}
		return json.Marshal(struct {
			Type ContentBlockType `json:"type"`
			*ContentBlockResource
		}{u.Type, u.Resource})
	default:
		return nil, fmt.Errorf("acp: invalid ContentBlock type: %s", string(u.Type))
	}
}

// UnmarshalJSON implements the ContentBlock discriminated union.
func (u *ContentBlock) UnmarshalJSON(data []byte) error {
	var d struct {
		Type ContentBlockType `json:"type"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	u.Type = d.Type
	switch d.Type {
	case ContentBlockTypeText:
		var v ContentBlockText
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Text = &v
	case ContentBlockTypeImage:
		var v ContentBlockImage
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Image = &v
	case ContentBlockTypeAudio:
		var v ContentBlockAudio
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Audio = &v
	case ContentBlockTypeResourceLink:
		var v ContentBlockResourceLink
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.ResourceLink = &v
	case ContentBlockTypeResource:
		var v ContentBlockResource
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Resource = &v
	default:
		return fmt.Errorf("acp: unknown ContentBlock type: %s", string(d.Type))
	}
	return nil
}

// --- ToolCallContent ---

// ToolCallContentType is ToolCallContent's discriminator (schema
// $defs/ToolCallContent's "type"). Only content/diff are modeled — the
// current schema's third variant, "terminal" (embedding a terminal/create
// result), is out of this slice's scope (terminal support is B1); an
// incoming terminal-content element fails to decode and is skipped by its
// caller (mapping.go's toolContentText), same as the pinned SDK's behavior
// before terminal existed at all.
type ToolCallContentType string

const (
	ToolCallContentTypeContent ToolCallContentType = "content"
	ToolCallContentTypeDiff    ToolCallContentType = "diff"
)

// ToolCallContentContent is the content variant's payload (schema
// $defs/Content).
type ToolCallContentContent struct {
	Content *ContentBlock `json:"content,omitempty"`
}

// ToolCallContent is one item of content a tool call produced (schema
// $defs/ToolCallContent).
type ToolCallContent struct {
	Type ToolCallContentType `json:"type"`

	Content *ToolCallContentContent
	Diff    *Diff
}

// MarshalJSON implements the ToolCallContent discriminated union.
func (u ToolCallContent) MarshalJSON() ([]byte, error) {
	switch u.Type {
	case ToolCallContentTypeContent:
		if u.Content == nil {
			return nil, fmt.Errorf("acp: ToolCallContent content field is required for type content")
		}
		return json.Marshal(struct {
			Type ToolCallContentType `json:"type"`
			*ToolCallContentContent
		}{u.Type, u.Content})
	case ToolCallContentTypeDiff:
		if u.Diff == nil {
			return nil, fmt.Errorf("acp: ToolCallContent diff field is required for type diff")
		}
		return json.Marshal(struct {
			Type ToolCallContentType `json:"type"`
			*Diff
		}{u.Type, u.Diff})
	default:
		return nil, fmt.Errorf("acp: invalid ToolCallContent type: %s", string(u.Type))
	}
}

// UnmarshalJSON implements the ToolCallContent discriminated union.
func (u *ToolCallContent) UnmarshalJSON(data []byte) error {
	var d struct {
		Type ToolCallContentType `json:"type"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	u.Type = d.Type
	switch d.Type {
	case ToolCallContentTypeContent:
		var v ToolCallContentContent
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Content = &v
	case ToolCallContentTypeDiff:
		var v Diff
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Diff = &v
	default:
		return fmt.Errorf("acp: unknown ToolCallContent type: %s", string(d.Type))
	}
	return nil
}

// --- SessionUpdate ---

// SessionUpdateType is SessionUpdate's discriminator (schema
// $defs/SessionUpdate's "sessionUpdate"). available_commands_update,
// config_option_update, and session_info_update are current, real spec
// variants NOT modeled here: the first two are out of this slice's scope
// (B4-slice commands, CO1 config options); the current schema's
// session_info_update means something entirely different from ctxloom's
// own hand-rolled notification of the same wire name (session metadata —
// title/updatedAt — vs. ctxloom's model/permissionMode/contextWindow/
// mcpServers header) — see the L0 checklist's B3. Rather than collide with
// it, ctxloom's own notification is emitted under a DIFFERENT, clearly
// vendor-scoped name (see internal/acpagent/wire.go's ctxloomSessionInfoUpdate),
// so it is deliberately absent from this union too; a real peer's genuine
// session_info_update (title/updatedAt) would fail to decode here today —
// acceptable for the same reason available_commands_update/
// config_option_update are absent: out of scope, and the caller
// warn-and-drops rather than crashing.
type SessionUpdateType string

const (
	SessionUpdateTypeUserMessageChunk  SessionUpdateType = "user_message_chunk"
	SessionUpdateTypeAgentMessageChunk SessionUpdateType = "agent_message_chunk"
	SessionUpdateTypeAgentThoughtChunk SessionUpdateType = "agent_thought_chunk"
	SessionUpdateTypeToolCall          SessionUpdateType = "tool_call"
	SessionUpdateTypeToolCallUpdate    SessionUpdateType = "tool_call_update"
	SessionUpdateTypePlan              SessionUpdateType = "plan"
	SessionUpdateTypeCurrentModeUpdate SessionUpdateType = "current_mode_update"
	SessionUpdateTypeUsageUpdate       SessionUpdateType = "usage_update"
)

// SessionUpdateUserMessageChunk/AgentMessageChunk/AgentThoughtChunk are the
// *_chunk variants' shared payload shape (schema $defs/ContentChunk,
// simplified to just the content block ctxloom actually reads/writes;
// ContentChunk's optional messageId is not modeled — ctxloom does not group
// chunks by message).
type (
	SessionUpdateUserMessageChunk struct {
		Content *ContentBlock `json:"content,omitempty"`
	}
	SessionUpdateAgentMessageChunk struct {
		Content *ContentBlock `json:"content,omitempty"`
	}
	SessionUpdateAgentThoughtChunk struct {
		Content *ContentBlock `json:"content,omitempty"`
	}
)

// SessionUpdateToolCall is the tool_call variant's payload (schema
// $defs/ToolCall). Kind/Status/Content/Locations are now PROPERLY TYPED
// (the pinned SDK's union file left several of these interface{} or
// []interface{} — see api/unions_generated.go's SessionUpdateToolCall in the
// old module — forcing callers to re-marshal/type-assert defensively;
// mapping.go's toolContentText no longer needs to).
type SessionUpdateToolCall struct {
	ToolCallId ToolCallId         `json:"toolCallId"`
	Title      string             `json:"title"`
	Kind       *ToolKind          `json:"kind,omitempty"`
	Status     *ToolCallStatus    `json:"status,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   interface{}        `json:"rawInput,omitempty"`
	RawOutput  interface{}        `json:"rawOutput,omitempty"`
}

// SessionUpdateToolCallUpdate is the tool_call_update variant's payload
// (schema $defs/ToolCallUpdate, mirrored here rather than reusing the
// standalone ToolCallUpdate type directly so the SessionUpdate variant can
// carry its own pointer semantics per field).
type SessionUpdateToolCallUpdate struct {
	ToolCallId ToolCallId         `json:"toolCallId"`
	Kind       *ToolKind          `json:"kind,omitempty"`
	Status     *ToolCallStatus    `json:"status,omitempty"`
	Title      *string            `json:"title,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   interface{}        `json:"rawInput,omitempty"`
	RawOutput  interface{}        `json:"rawOutput,omitempty"`
}

// SessionUpdatePlan is the plan variant's payload (schema $defs/Plan).
// Entries is now PROPERLY TYPED as []PlanEntry (the pinned SDK left it
// []interface{} — see api/unions_generated.go's SessionUpdatePlan in the old
// module — forcing mapping.go's mapPlan to re-marshal each element through
// the internal `remarshal` helper; it now iterates PlanEntry values
// directly).
type SessionUpdatePlan struct {
	Entries []PlanEntry `json:"entries"`
}

// CurrentModeUpdate is the current_mode_update variant's payload (schema
// $defs/CurrentModeUpdate) — collapses the hand-rolled anonymous struct
// internal/acpagent/wire.go's currentModeUpdateWire used to build ad hoc.
type CurrentModeUpdate struct {
	CurrentModeId SessionModeId `json:"currentModeId"`
}

// UsageUpdate is the usage_update variant's payload (schema
// $defs/UsageUpdate) — collapses the hand-rolled usageUpdate/usageUpdateWire
// pair (internal/acpagent/wire.go, internal/acp/mapping.go) H1 confirmed
// already matched this shape exactly.
type UsageUpdate struct {
	Used int   `json:"used"`
	Size int   `json:"size"`
	Cost *Cost `json:"cost,omitempty"`
}

// SessionUpdate is one session/update notification's `update` payload
// (schema $defs/SessionUpdate).
type SessionUpdate struct {
	Type SessionUpdateType `json:"sessionUpdate"`

	UserMessageChunk  *SessionUpdateUserMessageChunk
	AgentMessageChunk *SessionUpdateAgentMessageChunk
	AgentThoughtChunk *SessionUpdateAgentThoughtChunk
	ToolCall          *SessionUpdateToolCall
	ToolCallUpdate    *SessionUpdateToolCallUpdate
	Plan              *SessionUpdatePlan
	CurrentModeUpdate *CurrentModeUpdate
	UsageUpdate       *UsageUpdate
}

// MarshalJSON implements the SessionUpdate discriminated union.
func (u SessionUpdate) MarshalJSON() ([]byte, error) { //nolint:gocyclo // one discriminated union, exhaustive by construction
	switch u.Type {
	case SessionUpdateTypeUserMessageChunk:
		if u.UserMessageChunk == nil {
			return nil, fmt.Errorf("acp: SessionUpdate userMessageChunk field is required for type user_message_chunk")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*SessionUpdateUserMessageChunk
		}{u.Type, u.UserMessageChunk})
	case SessionUpdateTypeAgentMessageChunk:
		if u.AgentMessageChunk == nil {
			return nil, fmt.Errorf("acp: SessionUpdate agentMessageChunk field is required for type agent_message_chunk")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*SessionUpdateAgentMessageChunk
		}{u.Type, u.AgentMessageChunk})
	case SessionUpdateTypeAgentThoughtChunk:
		if u.AgentThoughtChunk == nil {
			return nil, fmt.Errorf("acp: SessionUpdate agentThoughtChunk field is required for type agent_thought_chunk")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*SessionUpdateAgentThoughtChunk
		}{u.Type, u.AgentThoughtChunk})
	case SessionUpdateTypeToolCall:
		if u.ToolCall == nil {
			return nil, fmt.Errorf("acp: SessionUpdate toolCall field is required for type tool_call")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*SessionUpdateToolCall
		}{u.Type, u.ToolCall})
	case SessionUpdateTypeToolCallUpdate:
		if u.ToolCallUpdate == nil {
			return nil, fmt.Errorf("acp: SessionUpdate toolCallUpdate field is required for type tool_call_update")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*SessionUpdateToolCallUpdate
		}{u.Type, u.ToolCallUpdate})
	case SessionUpdateTypePlan:
		if u.Plan == nil {
			return nil, fmt.Errorf("acp: SessionUpdate plan field is required for type plan")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*SessionUpdatePlan
		}{u.Type, u.Plan})
	case SessionUpdateTypeCurrentModeUpdate:
		if u.CurrentModeUpdate == nil {
			return nil, fmt.Errorf("acp: SessionUpdate currentModeUpdate field is required for type current_mode_update")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*CurrentModeUpdate
		}{u.Type, u.CurrentModeUpdate})
	case SessionUpdateTypeUsageUpdate:
		if u.UsageUpdate == nil {
			return nil, fmt.Errorf("acp: SessionUpdate usageUpdate field is required for type usage_update")
		}
		return json.Marshal(struct {
			Type SessionUpdateType `json:"sessionUpdate"`
			*UsageUpdate
		}{u.Type, u.UsageUpdate})
	default:
		return nil, fmt.Errorf("acp: invalid SessionUpdate type: %s", string(u.Type))
	}
}

// UnmarshalJSON implements the SessionUpdate discriminated union.
func (u *SessionUpdate) UnmarshalJSON(data []byte) error { //nolint:gocyclo // one discriminated union, exhaustive by construction
	var d struct {
		Type SessionUpdateType `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	u.Type = d.Type
	switch d.Type {
	case SessionUpdateTypeUserMessageChunk:
		var v SessionUpdateUserMessageChunk
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.UserMessageChunk = &v
	case SessionUpdateTypeAgentMessageChunk:
		var v SessionUpdateAgentMessageChunk
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.AgentMessageChunk = &v
	case SessionUpdateTypeAgentThoughtChunk:
		var v SessionUpdateAgentThoughtChunk
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.AgentThoughtChunk = &v
	case SessionUpdateTypeToolCall:
		var v SessionUpdateToolCall
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.ToolCall = &v
	case SessionUpdateTypeToolCallUpdate:
		var v SessionUpdateToolCallUpdate
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.ToolCallUpdate = &v
	case SessionUpdateTypePlan:
		var v SessionUpdatePlan
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.Plan = &v
	case SessionUpdateTypeCurrentModeUpdate:
		var v CurrentModeUpdate
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.CurrentModeUpdate = &v
	case SessionUpdateTypeUsageUpdate:
		var v UsageUpdate
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		u.UsageUpdate = &v
	default:
		return fmt.Errorf("acp: unknown SessionUpdate type: %s", string(d.Type))
	}
	return nil
}
