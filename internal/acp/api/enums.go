package api

// PermissionOptionKind hints at the nature of a permission option (schema
// $defs/PermissionOptionKind).
type PermissionOptionKind string

const (
	PermissionOptionKindAllowOnce    PermissionOptionKind = "allow_once"
	PermissionOptionKindAllowAlways  PermissionOptionKind = "allow_always"
	PermissionOptionKindRejectOnce   PermissionOptionKind = "reject_once"
	PermissionOptionKindRejectAlways PermissionOptionKind = "reject_always"
)

// StopReason is why an agent stopped processing a prompt turn (schema
// $defs/StopReason).
type StopReason string

const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
	StopReasonRefusal         StopReason = "refusal"
	StopReasonCancelled       StopReason = "cancelled"
)

// ToolCallStatus is a tool call's execution status (schema $defs/ToolCallStatus).
type ToolCallStatus string

const (
	ToolCallStatusPending    ToolCallStatus = "pending"
	ToolCallStatusInProgress ToolCallStatus = "in_progress"
	ToolCallStatusCompleted  ToolCallStatus = "completed"
	ToolCallStatusFailed     ToolCallStatus = "failed"
)

// ToolKind categorizes a tool call for icon/UI purposes (schema $defs/ToolKind).
type ToolKind string

const (
	ToolKindRead       ToolKind = "read"
	ToolKindEdit       ToolKind = "edit"
	ToolKindDelete     ToolKind = "delete"
	ToolKindMove       ToolKind = "move"
	ToolKindSearch     ToolKind = "search"
	ToolKindExecute    ToolKind = "execute"
	ToolKindThink      ToolKind = "think"
	ToolKindFetch      ToolKind = "fetch"
	ToolKindSwitchMode ToolKind = "switch_mode"
	ToolKindOther      ToolKind = "other"
)

// PlanEntryPriority is a plan entry's relative importance (schema
// $defs/PlanEntryPriority).
type PlanEntryPriority string

const (
	PlanEntryPriorityHigh   PlanEntryPriority = "high"
	PlanEntryPriorityMedium PlanEntryPriority = "medium"
	PlanEntryPriorityLow    PlanEntryPriority = "low"
)

// PlanEntryStatus is a plan entry's lifecycle status (schema
// $defs/PlanEntryStatus).
type PlanEntryStatus string

const (
	PlanEntryStatusPending    PlanEntryStatus = "pending"
	PlanEntryStatusInProgress PlanEntryStatus = "in_progress"
	PlanEntryStatusCompleted  PlanEntryStatus = "completed"
)
