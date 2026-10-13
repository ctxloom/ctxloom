package engine

// ToolLess is the marker an engine implements when it can run a session that
// grants the model NO tool and attaches NO MCP server: an internal one-shot
// that only reads the text it is handed and answers (a distillation, a
// compaction). Such a run is not plan: nothing it could do needs approving,
// so it carries no approver and no plan posture, and no approval route.
//
// The marker is the proof. A tool-less launch runs only on an engine that
// implements it (launch.Resolve refuses one that does not, and so does the
// runner that binds the session), and the implementation is the engine's own
// no-tools launch: every process the instance it returns composes — Exec and
// each structured turn — is that launch, whatever the session's posture
// document says. It is a method rather than a bare tag so that an engine
// cannot claim the marker without supplying the launch that honours it; each
// implementation's emitted argv and env are pinned by its own unit tests.
type ToolLess interface {
	Engine
	// ToolLess binds one session to run tool-less, as Instance binds one
	// at its posture. It refuses what Instance refuses (a required surface
	// the definition lacks).
	ToolLess(s Session) (Instance, error)
}
