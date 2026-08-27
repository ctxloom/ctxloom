package operations

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// ResolveTurnTranscript resolves WHICH reader and WHICH source a turn-boundary
// hook (TurnEnd next-step capture, the turn-changed guard) must use to read
// the transcript of the turn now ending on harp.
//
// It exists so those hooks route by the session's ACTUAL engine instead of
// assuming one. It reads vendorReaderRegistry — the same registry
// convertVendorTranscript reads — so a hook and a canonical conversion can
// never disagree about which parser an engine's bytes get, and no second
// engine-identity roster is minted (the four that already exist are
// enumerated in tests/arch/engine_identity_arch_test.go).
//
// The returned src is the ADAPTER'S OWN LOCATOR, not necessarily a file path:
// kiro's is a "<db-path>#<conversation-id>" composite, which is exactly why a
// hook cannot simply pass the payload's transcript_path through for every
// engine.
//
// hookTranscriptPath — the transcript_path the engine put in its own hook
// payload — WINS when it names a file that exists. It is the freshest possible
// locator: it is the file the turn was just written to, stated by the engine
// itself at the moment it ended, whereas the registry's locate resolves the
// binding recorded back at SessionStart. Anything else (an engine whose
// turn-boundary payload carries no path at all, a path that has since been
// rotated away) falls back to the registry, which is the only route that can
// serve an engine with no per-session file.
//
// Selection happens AFTER the source is known, matching
// convertVendorTranscript: an unknown-version refusal is a real, actionable
// signal and should only be raised about a session that actually has a
// transcript to read.
func ResolveTurnTranscript(ctx context.Context, harp, hookTranscriptPath string) (vendorreader.VendorAdapter, string, error) {
	entry, err := GetSession(harp)
	if err != nil {
		return nil, "", fmt.Errorf("look up session %s: %w", harp, err)
	}
	if entry == nil {
		return nil, "", fmt.Errorf("%s is not an indexed session, so there is no engine to select a transcript reader for", harp)
	}
	reg, ok := vendorReaderRegistry[entry.Backend]
	if !ok {
		return nil, "", fmt.Errorf("ctxloom carries no transcript reader for engine %q, so %s's turn cannot be read", entry.Backend, harp)
	}

	src := strings.TrimSpace(hookTranscriptPath)
	if src != "" {
		if _, statErr := os.Stat(src); statErr != nil {
			src = ""
		}
	}
	if src == "" {
		located, found := reg.locate(ctx, *entry)
		if !found {
			return nil, "", fmt.Errorf("no %s transcript could be located for %s", entry.Backend, harp)
		}
		src = located
	}

	adapter, aerr := vendorreader.SelectAdapter(entry.Backend, entry.EngineVersion, harp, reg.adapters)
	if aerr != nil {
		return nil, "", aerr
	}
	return adapter, src, nil
}
