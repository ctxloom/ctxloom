package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// The read-only start-session banner `ctxloom run` prints between minting a
// harp and spawning the engine. It belongs to the RUN path, not the `session`
// command tree; it lives beside the session code only because every field it
// shows is session-index data.

// StartSessionInfo is the read-only pre-spawn summary `ctxloom run` prints
// just before launching the engine (WS-5, Decision 11): this session's harp,
// an assembled-context summary, and a pointer to the previous session for
// this project. Every field is something the caller already computed for
// THIS launch (context assembly, harp assignment) or resolved via the SAME
// primitive the get_previous_session MCP tool reads
// (operations.ResolvePreviousSession, internal/operations/sessions.go) — the
// banner never re-derives a summary of its own.
type StartSessionInfo struct {
	Harp      string
	Backend   string
	Label     string
	Profiles  []string
	Fragments []string
	Tokens    int
	// Previous is the project's prior session, or nil when there is none
	// (first run in this project). Informational only — startup no longer
	// offers to resume it (Decision 11); the in-engine "resume" skill
	// (recover_session/load_session/get_previous_session) is how a user
	// actually pulls it back, from inside the session that just started.
	Previous *operations.PreviousSessionRef
}

// PrintStartSessionBanner renders info to w as the read-only start-session
// display: printed AFTER a fresh harp is minted and BEFORE the engine spawns.
// It replaces the old interactive resume picker (which used to occupy this
// same moment in startup) with a plain, non-interactive summary — startup no
// longer asks the user anything about resuming; it just reports what this
// session is. Every field is optional so a sparsely-populated StartSessionInfo
// (e.g. context assembly or harp assignment degraded per CLAUDE.md fault
// tolerance) still renders a useful, non-erroring banner.
func PrintStartSessionBanner(w io.Writer, info StartSessionInfo) {
	label := info.Label
	if label == "" {
		label = info.Backend
	}
	if label != "" {
		fmt.Fprintf(w, "ctxloom: starting session %s (%s)\n", info.Harp, label)
	} else {
		fmt.Fprintf(w, "ctxloom: starting session %s\n", info.Harp)
	}
	if len(info.Profiles) > 0 {
		fmt.Fprintf(w, "  profiles: %s\n", strings.Join(info.Profiles, ", "))
	}
	switch {
	case len(info.Fragments) > 0:
		fmt.Fprintf(w, "  context: %d fragment(s), ~%d tokens\n", len(info.Fragments), info.Tokens)
	case info.Tokens > 0:
		fmt.Fprintf(w, "  context: ~%d tokens\n", info.Tokens)
	}
	if info.Previous != nil && info.Previous.Harp != "" {
		fmt.Fprintf(w, "  previous session: %s — bring it back in-session with the \"resume\" skill\n", info.Previous.Harp)
	}
}
