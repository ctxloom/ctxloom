package bundles

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Loadout is what a companion binary advertises about itself: ONE signed
// document carrying TWO loadouts with different lifecycles.
//
// Run is consumed every session — the bundle of fragments, commands, skills,
// hooks and MCP servers the companion contributes. Init is consumed once, at
// setup. They are separate top-level sections of one document, under one
// signature, so a companion's setup-time and session-time contributions can
// never be signed, delivered or reviewed apart from each other.
type Loadout struct {
	// Run is the RUN loadout: the bundle. Never nil on a parsed loadout — a
	// document without `run:` parses to an empty bundle, so a reader can seed
	// it without a nil check at every use.
	Run *Bundle
	// Init is the INIT loadout; the zero value when the document declares
	// no `init:` section.
	Init InitLoadout
}

// InitLoadout is the setup-time half of a companion's loadout. Every field is
// TYPED and read by name — none of them is a command with a well-known name.
// That is the whole reason the section exists: the previous convention
// (a command literally called `agent-setup` or `tooling`) was undiscoverable,
// unvalidated and silently no-op'd on a typo, and no companion ever shipped
// one. A typed field is refused at parse when misspelled (see ParseLoadout's
// strictness) and is listable without knowing a magic name.
type InitLoadout struct {
	// SetupGuidance is text spliced into ctxloom's init prompt AFTER the
	// built-in setup body, so the companion can steer how it is configured
	// for a project. It augments the built-in; nothing replaces it.
	SetupGuidance string `yaml:"setup_guidance,omitempty"`
	// Tooling declares the tools the companion's content needs available
	// where agents run (the agent container image), as text for
	// `ctxloom tooling` to collect and the human to fold into the base
	// Containerfile.
	Tooling string `yaml:"tooling,omitempty"`
	// LegacyContext names where this companion's PRE-EXISTING project
	// context lives (paths, project-relative), for init to ingest. The
	// companion answers this as a lookup it already knows, never as a
	// judgment about the project.
	LegacyContext []string `yaml:"legacy_context,omitempty"`
	// Questions are what init should put to the human on this companion's
	// behalf before configuring it.
	Questions []InitQuestion `yaml:"questions,omitempty"`
}

// IsZero reports whether the INIT loadout declares nothing at all, so a
// consumer can skip a companion that only speaks at session time.
func (i InitLoadout) IsZero() bool {
	return i.SetupGuidance == "" && i.Tooling == "" && len(i.LegacyContext) == 0 && len(i.Questions) == 0
}

// InitQuestion is one question a companion asks init to put to the human.
type InitQuestion struct {
	// ID keys the answer; stable across releases of the companion.
	ID string `yaml:"id"`
	// Prompt is the question as the human sees it.
	Prompt string `yaml:"prompt"`
}

// loadoutDocument is the on-the-wire shape ParseLoadout decodes. Run stays a
// yaml.Node rather than a Bundle so the RUN section reaches ParseBundle as
// bytes and takes exactly the path every other bundle takes — the schema
// upgrades, the legacy-skills guard, strict decoding, link and MCP checks,
// the empty floor — rather than a second decode that would drift from it.
type loadoutDocument struct {
	Run  yaml.Node   `yaml:"run"`
	Init InitLoadout `yaml:"init"`
}

// ParseLoadout parses a companion's loadout DOCUMENT — the exact bytes its
// detached signature covers — into its RUN bundle and typed INIT loadout.
//
// STRICT at every level: a key the document does not model is refused, not
// dropped, and a bare bundle at the top level (the previous contract's
// payload shape) is refused as an unknown-key error rather than read as a
// loadout with an empty RUN section. A document that declares neither section
// contributed nothing and is refused the way ParseBundle refuses an empty
// bundle, for the same reason: a loadout contributing nothing must fail loud,
// not parse successfully.
func ParseLoadout(data []byte) (*Loadout, error) {
	var doc loadoutDocument
	if err := yamlx.DecodeStrict(data, &doc); err != nil {
		return nil, strictDecodeError(err)
	}
	lo := &Loadout{Init: doc.Init}
	if doc.Run.Kind == 0 {
		if lo.Init.IsZero() {
			return nil, fmt.Errorf("loadout is empty: %d bytes parsed to a document declaring neither a run: nor an init: section", len(data))
		}
		lo.Run = emptyBundle()
		return lo, nil
	}
	runBytes, err := yaml.Marshal(&doc.Run)
	if err != nil {
		return nil, fmt.Errorf("loadout run section: %w", err)
	}
	run, err := ParseBundle(runBytes)
	if err != nil {
		return nil, fmt.Errorf("loadout run section: %w", err)
	}
	lo.Run = run
	return lo, nil
}
