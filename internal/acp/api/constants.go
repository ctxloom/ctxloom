package api

// Method name constants (schema-v1.19.0's x-method annotations for the
// request/notification shapes ctxloom's client and agent roles use).
const (
	MethodInitialize               = "initialize"
	MethodSessionNew               = "session/new"
	MethodSessionLoad              = "session/load"
	MethodSessionPrompt            = "session/prompt"
	MethodSessionCancel            = "session/cancel"
	MethodSessionUpdate            = "session/update"
	MethodSessionSetMode           = "session/set_mode"
	MethodSessionRequestPermission = "session/request_permission"
	MethodFsReadTextFile           = "fs/read_text_file"
	MethodFsWriteTextFile          = "fs/write_text_file"
)

// ACPProtocolVersion is the protocol version ctxloom negotiates (v1).
const ACPProtocolVersion = 1
