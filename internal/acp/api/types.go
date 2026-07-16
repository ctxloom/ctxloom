package api

// This file defines the plain (non-discriminated-union) ACP object shapes
// ctxloom's client (internal/acp) and agent (internal/acpagent) roles use.
// See doc.go for scope and provenance; unions.go carries the discriminated
// unions (ContentBlock, SessionUpdate, ToolCallContent).

// SessionId identifies a conversation session (schema $defs/SessionId).
type SessionId string

// ToolCallId identifies one tool call within a session (schema
// $defs/ToolCallId).
type ToolCallId string

// AuthMethodId identifies an authentication method (schema
// $defs/AuthMethodId). ctxloom does not implement authentication (C3,
// tracked separately) — this exists only so InitializeResponse.AuthMethods
// is expressible; the agent side never populates it.
type AuthMethodId string

// Implementation describes the name/version (and optional title) of a client
// or agent (schema $defs/Implementation) — the initialize handshake's
// clientInfo/agentInfo. This COLLAPSES the hand-rolled clientInfoBlock
// (internal/acp/session.go) and agentInfoBlock (internal/acpagent/wire.go)
// the pinned SDK predated.
type Implementation struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitempty"`
	Version string  `json:"version"`
}

// AuthMethod describes one authentication method the agent advertises
// (schema $defs/AuthMethod). Unused today (see AuthMethodId).
type AuthMethod struct {
	Id          AuthMethodId `json:"id"`
	Name        string       `json:"name"`
	Description *string      `json:"description,omitempty"`
}

// PromptCapabilities are the content types an agent accepts in
// session/prompt beyond the text/resource_link baseline (schema
// $defs/PromptCapabilities).
type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// McpCapabilities are the MCP transports an agent supports beyond the
// baseline stdio transport (schema $defs/McpCapabilities). ctxloom only ever
// launches stdio MCP servers today (HTTP/SSE is a tracked follow-on, out of
// this slice's scope), so both fields are always false — but the TYPE now
// exists, which is what B5 (the L0 checklist) asked for.
type McpCapabilities struct {
	Http bool `json:"http,omitempty"`
	Sse  bool `json:"sse,omitempty"`
}

// The following are capability marker types: a nil pointer means "not
// advertised", a non-nil pointer (even to a zero value) means "advertised".
// None carry fields today (schema-v1.19.0) beyond the reserved _meta.
type (
	// SessionListCapabilities marks session/list support.
	SessionListCapabilities struct{}
	// SessionDeleteCapabilities marks session/delete support.
	SessionDeleteCapabilities struct{}
	// SessionAdditionalDirectoriesCapabilities marks additionalDirectories support.
	SessionAdditionalDirectoriesCapabilities struct{}
	// SessionResumeCapabilities marks session/resume support.
	SessionResumeCapabilities struct{}
	// SessionCloseCapabilities marks session/close support.
	SessionCloseCapabilities struct{}
	// LogoutCapabilities marks the logout method's support.
	LogoutCapabilities struct{}
)

// SessionCapabilities are the optional session-lifecycle methods an agent
// supports beyond the baseline new/prompt/cancel/update (schema
// $defs/SessionCapabilities). ctxloom implements none of these yet (session
// list/delete/resume/close are a tracked follow-on) — the type exists so
// AgentCapabilities can express it (B5); every field stays nil until that
// slice lands.
type SessionCapabilities struct {
	List                  *SessionListCapabilities                  `json:"list,omitempty"`
	Delete                *SessionDeleteCapabilities                `json:"delete,omitempty"`
	AdditionalDirectories *SessionAdditionalDirectoriesCapabilities `json:"additionalDirectories,omitempty"`
	Resume                *SessionResumeCapabilities                `json:"resume,omitempty"`
	Close                 *SessionCloseCapabilities                 `json:"close,omitempty"`
}

// AgentAuthCapabilities are the authentication-related methods an agent
// supports (schema $defs/AgentAuthCapabilities). ctxloom implements no
// authentication (C3, tracked separately) — Logout stays nil.
type AgentAuthCapabilities struct {
	Logout *LogoutCapabilities `json:"logout,omitempty"`
}

// AgentCapabilities are the capabilities an agent advertises at
// initialization (schema $defs/AgentCapabilities). McpCapabilities,
// SessionCapabilities, and AgentAuthCapabilities are NEW relative to the
// pinned SDK's AgentCapabilities (which had only LoadSession +
// PromptCapabilities, see api/types_generated.go:12-18 in the old module) —
// this is what the L0 checklist's B5 asked the re-vendor to make possible.
// Advertising anything beyond LoadSession/PromptCapabilities is C1's job, not
// this slice's; every new field is a zero value today.
type AgentCapabilities struct {
	LoadSession         bool                   `json:"loadSession,omitempty"`
	PromptCapabilities  PromptCapabilities     `json:"promptCapabilities,omitempty"`
	McpCapabilities     McpCapabilities        `json:"mcpCapabilities,omitempty"`
	SessionCapabilities *SessionCapabilities   `json:"sessionCapabilities,omitempty"`
	Auth                *AgentAuthCapabilities `json:"auth,omitempty"`
}

// FileSystemCapabilities are the fs/* methods a client supports (schema
// $defs/FileSystemCapabilities — renamed from the pinned SDK's singular
// FileSystemCapability to match the current schema's def name).
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// ClientCapabilities are the capabilities a client advertises at
// initialization (schema $defs/ClientCapabilities).
type ClientCapabilities struct {
	Fs       FileSystemCapabilities `json:"fs,omitempty"`
	Terminal bool                   `json:"terminal,omitempty"`
}

// InitializeRequest is the initialize request body (schema
// $defs/InitializeRequest). This COLLAPSES the hand-rolled initializeParams
// (internal/acp/session.go), which existed only because the pinned SDK's
// InitializeRequest predated ClientInfo.
type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities,omitempty"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

// InitializeResponse is the initialize response body (schema
// $defs/InitializeResponse). This COLLAPSES the hand-rolled initializeResult
// (internal/acpagent/wire.go), which existed only because the pinned SDK's
// InitializeResponse predated AgentInfo.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

// EnvVariable is one environment variable to set when launching a stdio MCP
// server (schema $defs/EnvVariable).
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// McpServer is a stdio-transport MCP server configuration (schema
// $defs/McpServerStdio — the ONLY McpServer variant ctxloom constructs or
// accepts today; the current schema's McpServer is technically a
// discriminated union of http/sse/stdio, but stdio is the untagged default
// arm (no "type" property), so this flat struct already marshals/unmarshals
// as a schema-valid stdio McpServer with zero change from the pinned SDK's
// shape. HTTP/SSE transports are a tracked follow-on, out of this slice's
// scope.
type McpServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []EnvVariable `json:"env"`
}

// NewSessionRequest is the session/new request body (schema
// $defs/NewSessionRequest).
type NewSessionRequest struct {
	Cwd        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

// NewSessionResponse is the session/new response body (schema
// $defs/NewSessionResponse). ctxloom's modes/models state
// (internal/acpagent/wire.go's newSessionResult) rides alongside this as a
// separate struct at the call site rather than embedded fields here, since
// `modes` collapses onto SessionModeState (this package) but `models` is a
// ctxloom-only, not-yet-a-spec-construct extension (see the B4 note in
// wire.go).
type NewSessionResponse struct {
	SessionId SessionId `json:"sessionId"`
}

// LoadSessionRequest is the session/load request body (schema
// $defs/LoadSessionRequest).
type LoadSessionRequest struct {
	McpServers []McpServer `json:"mcpServers"`
	Cwd        string      `json:"cwd"`
	SessionId  SessionId   `json:"sessionId"`
}

// CancelNotification is the session/cancel notification body (schema
// $defs/CancelNotification).
type CancelNotification struct {
	SessionId SessionId `json:"sessionId"`
}

// PromptRequestPromptElem is one element of a PromptRequest.Prompt (a
// ContentBlock). Named to match the pinned SDK's identifier so
// []api.PromptRequestPromptElem{api.ContentBlock{...}} call sites keep
// compiling unchanged.
type PromptRequestPromptElem = ContentBlock

// PromptRequest is the session/prompt request body (schema
// $defs/PromptRequest).
type PromptRequest struct {
	SessionId SessionId                 `json:"sessionId"`
	Prompt    []PromptRequestPromptElem `json:"prompt"`
}

// PromptResponse is the session/prompt response body (schema
// $defs/PromptResponse). StopReason is now PROPERLY TYPED as StopReason
// (the pinned SDK typed this interface{} — see
// api/types_generated.go:518-524 in the old module — because its generator
// couldn't resolve the enum through the response wrapper); callers that used
// to route it through the loosely-typed rawText() helper now just convert
// with string(resp.StopReason).
type PromptResponse struct {
	StopReason StopReason `json:"stopReason"`
}

// PermissionOption is one option offered to the user in a permission request
// (schema $defs/PermissionOption).
type PermissionOption struct {
	OptionId PermissionOptionId   `json:"optionId"`
	Name     string               `json:"name"`
	Kind     PermissionOptionKind `json:"kind"`
}

// PermissionOptionId identifies a permission option (schema
// $defs/PermissionOptionId).
type PermissionOptionId string

// ToolCallLocation is a file location a tool call affects (schema
// $defs/ToolCallLocation).
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// ToolCallUpdate describes/updates one tool call (schema
// $defs/ToolCallUpdate) — used standalone as RequestPermissionRequest's
// embedded toolCall, and as the payload of a tool_call_update SessionUpdate
// (see unions.go's SessionUpdateToolCallUpdate, which mirrors these fields).
type ToolCallUpdate struct {
	ToolCallId ToolCallId         `json:"toolCallId"`
	Kind       *ToolKind          `json:"kind,omitempty"`
	Status     *ToolCallStatus    `json:"status,omitempty"`
	Title      *string            `json:"title,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   interface{}        `json:"rawInput,omitempty"`
	RawOutput  interface{}        `json:"rawOutput,omitempty"`
}

// RequestPermissionRequest is the session/request_permission request body
// (schema $defs/RequestPermissionRequest). Options is a plain (non-pointer)
// slice with no omitempty: the schema REQUIRES options to be an array (see
// L0 checklist A2) — a caller that builds this with a nil slice still
// marshals `"options":[]`, not `null` (see mapping.go's use of make(...,
// 0, ...) rather than a `var options []PermissionOption` declaration).
type RequestPermissionRequest struct {
	SessionId SessionId          `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// ReadTextFileRequest is the fs/read_text_file request body (schema
// $defs/ReadTextFileRequest).
type ReadTextFileRequest struct {
	Path  string `json:"path"`
	Line  *int   `json:"line,omitempty"`
	Limit *int   `json:"limit,omitempty"`
}

// ReadTextFileResponse is the fs/read_text_file response body (schema
// $defs/ReadTextFileResponse).
type ReadTextFileResponse struct {
	Content string `json:"content"`
}

// WriteTextFileRequest is the fs/write_text_file request body (schema
// $defs/WriteTextFileRequest).
type WriteTextFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteTextFileResponse is the fs/write_text_file response body (schema
// $defs/WriteTextFileResponse): an OBJECT with no required fields, never
// null. This is the L0 checklist's A1 fix — handleFsWrite now returns
// WriteTextFileResponse{} instead of a bare nil (which internal/acp/jsonrpc's
// marshalResult renders as literal JSON `null`, failing the spec's
// `"type":"object"`).
type WriteTextFileResponse struct{}

// PlanEntry is one task in an agent's execution plan (schema
// $defs/PlanEntry).
type PlanEntry struct {
	Content  string            `json:"content"`
	Priority PlanEntryPriority `json:"priority"`
	Status   PlanEntryStatus   `json:"status"`
}

// Diff is a file modification shown as a diff (schema $defs/Diff) — a
// ToolCallContent variant's payload (unions.go).
type Diff struct {
	Path    string  `json:"path"`
	OldText *string `json:"oldText,omitempty"`
	NewText string  `json:"newText"`
}

// Cost is a session's cumulative cost (schema $defs/Cost) — usage_update's
// optional payload.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// SessionModeId identifies a session mode (schema $defs/SessionModeId).
type SessionModeId = string

// SessionMode is one mode an agent can operate in (schema $defs/SessionMode).
// H1 confirmed ctxloom's hand-rolled modeWire (internal/acpagent/wire.go)
// matches this shape exactly; this is that type, now backed by the current
// schema instead of hand-rolled.
type SessionMode struct {
	Id          SessionModeId `json:"id"`
	Name        string        `json:"name"`
	Description *string       `json:"description,omitempty"`
}

// SessionModeState is the set of modes and the currently active one (schema
// $defs/SessionModeState) — collapses the hand-rolled modeState
// (internal/acpagent/wire.go).
type SessionModeState struct {
	CurrentModeId  SessionModeId `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SetSessionModeRequest is the session/set_mode request body (schema
// $defs/SetSessionModeRequest) — collapses the hand-rolled setModeParams
// (internal/acpagent/wire.go).
type SetSessionModeRequest struct {
	SessionId SessionId     `json:"sessionId"`
	ModeId    SessionModeId `json:"modeId"`
}

// SetSessionModeResponse is the session/set_mode response body (schema
// $defs/SetSessionModeResponse): an empty object.
type SetSessionModeResponse struct{}
