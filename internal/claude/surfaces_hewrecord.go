package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file is claude's HEW-RECORD writer: the second implementation of the
// writer seam (agent.Approach), beside the plain file writers in
// surfaces.go. The two differ in WHERE the presenter lands the bytes and in
// HOW Deliver acts, and nothing in the seam branches on which is which —
// that is what makes the seam polymorphic rather than a mode flag.
//
//	plain   (settingsSurface)   presents under the PROJECT ROOT; Deliver
//	                            merges through the ledger-tracked JSON writer.
//	record  (settingsRecord)    presents under the ENGINE HOME; Deliver
//	                            patches through hew and writes a §9.7 record
//	                            (confpatch) that remembers exactly what
//	                            ctxloom put there and how to take it out.
//
// It is the first delivery on the seam whose bytes are NOT a project file.

// ApproachHewRecord names claude's record-backed settings write into the
// engine's private config home. It is claude's own name, like
// ApproachSystemPrompt: no shared code refers to it. A binding selects it by
// name (surfaces: settings: hew-record); it is never a default, because the
// engine home it needs exists only for a run whose binding declared one.
const ApproachHewRecord = "hew-record"

// settingsRecord is claude's hew-record settings approach.
//
// Present lands <EngineHome>/settings.json — claude's USER-scope settings
// file, read from $CLAUDE_CONFIG_DIR — with no launch flag: claude finds it
// natively. Deliver refuses a Start with no engine home (ErrUnrootedEngineHome)
// rather than falling back to the user's real ~/.claude, which is exactly the
// shared, dangerous location a private home exists to keep agents out of.
//
// The desired settings are computed ONCE, by the same writer the plain
// approach uses (writeSettingsFile against an empty document), so the two
// writers cannot disagree about what ctxloom wants in a settings file; only
// the way it reaches the file differs. The instance file is SEEDED from the
// user's real settings, so ctxloom's hooks and deny entries interleave with
// theirs — the record, not a marker, is what tells them apart afterwards.
//
// Cleanup applies the record's reversal, restoring the seeded bytes exactly.
// That reverses ctxloom's deny entries too — a deliberate difference from
// the ledger writer's never-retract-a-denial policy: the engine home is a
// disposable per-session instance, and an exact reversal of an exact record
// is the contract Delivered promises.
type settingsRecord struct {
	hooks            *wire.HooksConfig
	manageStatusline bool
	denyTools        []string
	fs               afero.Fs
}

// Present declares the engine-home settings file. No flag.
func (s *settingsRecord) Present(start present.Start) present.Presentation {
	return start.UnderEngineHome(SettingsFileName).Build()
}

// Deliver patches ctxloom's settings into the engine-home file through the
// record store and returns the handle that reverses it.
func (s *settingsRecord) Deliver(start present.Start) (agent.Delivered, error) {
	if err := agent.EngineHomeRooted(start); err != nil {
		return nil, err
	}
	target := s.Present(start).HostPath
	desired, err := s.desired()
	if err != nil {
		return nil, err
	}
	// The same home-rooted §9.7 store claude's .mcp.json write applies through.
	store, err := (&ClaudeCodeHookWriter{FS: s.fs}).recordStore()
	if err != nil {
		return nil, err
	}
	if _, err := store.Apply(s.fs, target, desired.build); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", target, err)
	}
	return agent.DeliveredFunc(func() error {
		// The reversal alone: an empty build leaves the user with exactly
		// the file ctxloom found.
		_, err := store.Apply(s.fs, target, func(*hew.Doc, hew.Document) (int, error) { return 0, nil })
		return err
	}), nil
}

// desiredSettings is what ctxloom wants in a settings file, as generic JSON
// values: the hook matcher groups per event, the managed statusline, and
// the deny entries.
type desiredSettings struct {
	hooks      map[string][]any
	statusLine any
	deny       []string
}

// desired runs the plain settings writer against an EMPTY document in a
// throwaway filesystem and reads back what it produced — the one computation
// of "what ctxloom wants", reused rather than restated.
func (s *settingsRecord) desired() (desiredSettings, error) {
	scratch := afero.NewMemMapFs()
	w := &ClaudeCodeHookWriter{FS: scratch, statusLineDisabled: !s.manageStatusline}
	const dir = "/desired"
	if err := w.writeSettingsFile(s.hooks, s.denyTools, dir); err != nil {
		return desiredSettings{}, fmt.Errorf("computing the desired settings: %w", err)
	}
	raw, err := afero.ReadFile(scratch, w.SettingsPath(dir))
	if err != nil {
		return desiredSettings{}, fmt.Errorf("computing the desired settings: %w", err)
	}
	var parsed struct {
		Hooks       map[string][]any `json:"hooks"`
		StatusLine  any              `json:"statusLine"`
		Permissions *struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return desiredSettings{}, fmt.Errorf("computing the desired settings: %w", err)
	}
	out := desiredSettings{hooks: parsed.Hooks, statusLine: parsed.StatusLine}
	if parsed.Permissions != nil {
		out.deny = parsed.Permissions.Deny
	}
	return out, nil
}

// The JSON members ctxloom writes into.
const (
	settingsHooksKey       = "hooks"
	settingsStatusLineKey  = "statusLine"
	settingsPermissionsKey = "permissions"
	settingsDenyKey        = "deny"
)

// errHooksBesideExisting is returned when the engine-home settings already
// carry a hook array for an event ctxloom writes. A hook matcher group has
// no scalar identity member (claude's `matcher` is optional and absent for
// ctxloom's own groups), so hew addresses the array by index — and hew's
// renderer emits an index-addressed element removal in a form its own parser
// refuses (§6.4.2 demands a key). The reversal would be unusable, so
// confpatch would refuse the write anyway; refusing HERE says why, and what
// to do, instead of surfacing a parse error about a patch nobody wrote.
//
// It is exact, not conservative: ctxloom's own group can never carry an
// identity field, so no state of the existing array makes the insert
// recordable. The instance home is seeded with credentials only, never a
// settings.json, so the ordinary first write creates the file and never
// meets this; it is reached only when something else wrote hooks for the
// same event into the instance file between ctxloom's writes.
var errHooksBesideExisting = errors.New("the engine-home settings.json already carries hooks for this event, and a hook group cannot be recorded reversibly beside them (hew addresses a keyless hook array by index, which its reversal cannot express); remove those hooks from the instance file, or select settings=unsafe-file for this agent")

// build records ctxloom's entries against the RESTORED document (the user's
// file with ctxloom's previous entries already taken back out). Every op is
// an ADD beside the user's own entries — a hook event ctxloom does not find
// is created whole, a deny entry is inserted into the user's list, a
// container that is absent is created — so the user's hooks, statusline and
// deny entries are never replaced, and the record's reversal removes exactly
// what was added. The user's own statusline wins, as it does for the plain
// writer. A hook event that already exists is refused: see
// errHooksBesideExisting.
func (d desiredSettings) build(doc *hew.Doc, cur hew.Document) (int, error) {
	recorded := 0
	set := func(pointer string, v any) error {
		p, err := hew.ParsePathIn(doc.Format(), pointer)
		if err != nil {
			return err
		}
		doc.AtPath(p).Set(v)
		recorded++
		return nil
	}
	// insert inserts one element into the ARRAY at pointer. In hew's JSON
	// dialect an add addressed at an existing array is an element insert —
	// appended when no sibling is named — and that, not RFC 6901's "-"
	// position, is the shape its applier reads; "-" is refused as
	// inexpressible there.
	insert := func(pointer string, v any) error {
		p, err := hew.ParsePathIn(doc.Format(), pointer)
		if err != nil {
			return err
		}
		doc.AtPath(p).Add(v)
		recorded++
		return nil
	}
	root := cur.Root()

	if len(d.hooks) > 0 {
		hooks, ok := root.Member(settingsHooksKey)
		if !ok {
			if err := set("/"+settingsHooksKey, d.hooks); err != nil {
				return 0, err
			}
		} else {
			events := make([]string, 0, len(d.hooks))
			for e := range d.hooks {
				events = append(events, e)
			}
			sort.Strings(events) // stable order: a deterministic record
			for _, event := range events {
				if _, ok := hooks.Member(event); ok {
					return 0, fmt.Errorf("%s: %w", event, errHooksBesideExisting)
				}
				if err := set("/"+settingsHooksKey+"/"+escapePointerToken(event), d.hooks[event]); err != nil {
					return 0, err
				}
			}
		}
	}

	if d.statusLine != nil {
		if _, ok := root.Member(settingsStatusLineKey); !ok {
			if err := set("/"+settingsStatusLineKey, d.statusLine); err != nil {
				return 0, err
			}
		}
	}

	if len(d.deny) > 0 {
		perms, ok := root.Member(settingsPermissionsKey)
		if !ok {
			if err := set("/"+settingsPermissionsKey, map[string]any{settingsDenyKey: d.deny}); err != nil {
				return 0, err
			}
			return recorded, nil
		}
		deny, ok := perms.Member(settingsDenyKey)
		if !ok {
			if err := set("/"+settingsPermissionsKey+"/"+settingsDenyKey, d.deny); err != nil {
				return 0, err
			}
			return recorded, nil
		}
		present := map[string]bool{}
		for i := 0; i < deny.Len(); i++ {
			if el, ok := deny.Elem(i); ok {
				if n := el.Value().Node(); n != nil {
					present[n.Value] = true
				}
			}
		}
		for _, tool := range d.deny {
			if present[tool] {
				continue
			}
			if err := insert("/"+settingsPermissionsKey+"/"+settingsDenyKey, tool); err != nil {
				return 0, err
			}
		}
	}
	return recorded, nil
}

// escapePointerToken applies RFC 6901's token escaping so a member name
// containing "~" or "/" addresses that member rather than a path.
func escapePointerToken(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

var _ agent.Approach = (*settingsRecord)(nil)
