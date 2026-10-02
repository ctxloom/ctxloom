package bundles

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// ReadTree assembles a monolithic Bundle from a tree-form content.Bundle:
// the inverse of Convert, and the half the tree format shipped without.
//
// It exists because the rest of ctxloom still consumes a Bundle —
// assembly, the trust gate, profile resolution and materialize all take one —
// so a tree has to become one somewhere. Doing it HERE, in the package that
// already owns the other direction, is what keeps the two mappings adjacent:
// a field added to one kind's conversion and forgotten in the other is a
// round-trip test failure rather than a surface that silently stops arriving.
//
// It is NOT a dual-format reader (see this package's doc). Nothing calls it to
// fall back to a document; it is the ONE way tree bytes become a bundle value.
//
// THREE REFUSALS, all for the same reason: each alternative produces a bundle
// that loads, assembles and materializes perfectly happily while being wrong.
//   - no envelope — nothing else in the tree can supply a version, so the
//     bundle would claim one it does not have;
//   - an envelope that ALSO declares items inline — two answers for one item and
//     no rule for which wins, which is how a stale inline copy outlives the file
//     that superseded it;
//   - no items at all — an empty bundle delivers nothing, loudly to no one.
func ReadTree(ctx context.Context, b content.Bundle) (*Bundle, error) {
	out, err := readEnvelope(ctx, b)
	if err != nil {
		return nil, err
	}
	refs, err := b.Refs(ctx)
	if err != nil {
		return nil, fmt.Errorf("bundles: enumerating tree bundle %q: %w", b.ID(), err)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("bundles: tree bundle %q declares no items; refusing to read it as an empty bundle "+
			"(an empty bundle loads, assembles and materializes without complaint and delivers nothing)", b.ID())
	}

	r := &reader{bundle: string(b.ID()), out: out, hooks: map[string][]content.Hook{}}
	for _, ref := range refs {
		item, err := b.Item(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("bundles: reading %s: %w", ref.Key(), err)
		}
		surface, err := item.Surface(ctx)
		if err != nil {
			return nil, fmt.Errorf("bundles: decoding %s: %w", ref.Key(), err)
		}
		if err := r.add(ref, surface); err != nil {
			return nil, err
		}
	}
	r.finishHooks()
	return r.out, nil
}

// readEnvelope parses the tree's bundle.yaml into the bundle value the items are
// then filled into, and refuses one that still declares items inline.
func readEnvelope(ctx context.Context, b content.Bundle) (*Bundle, error) {
	raw, err := b.ReadFile(ctx, DirectoryFormManifest)
	if err != nil {
		return nil, fmt.Errorf("bundles: tree bundle %q has no %s, so it carries no version or description "+
			"and cannot be read as a bundle: %w", b.ID(), DirectoryFormManifest, err)
	}
	env, err := ParseBundle(raw)
	if err != nil {
		return nil, fmt.Errorf("bundles: parsing %s of tree bundle %q: %w", DirectoryFormManifest, b.ID(), err)
	}
	if inline := inlineKeys(env); len(inline) > 0 {
		return nil, fmt.Errorf("bundles: %s of tree bundle %q still declares %s inline while the tree also holds item files; "+
			"a half-migrated bundle has two answers for one item and no rule for which wins — "+
			"finish the migration by removing the inline keys", DirectoryFormManifest, b.ID(), strings.Join(inline, ", "))
	}
	return env, nil
}

// inlineKeys names the item-bearing envelope keys that are populated. It is
// exhaustive over the kinds Read fills in, which is what makes the ambiguity
// check total rather than a spot-check on whichever key someone remembered.
func inlineKeys(b *Bundle) []string {
	var out []string
	if len(b.Fragments) > 0 {
		out = append(out, "fragments")
	}
	if len(b.Commands) > 0 {
		out = append(out, "commands")
	}
	if len(b.MCP) > 0 {
		out = append(out, "mcp")
	}
	if len(b.Skills) > 0 {
		out = append(out, "skills")
	}
	if len(b.Profiles) > 0 {
		out = append(out, "profiles")
	}
	if b.Hooks.HasAny() {
		out = append(out, "hooks")
	}
	return out
}

// reader accumulates one tree read. Hooks are buffered rather than appended as
// they arrive because their EVENT ORDER is not their arrival order — see
// finishHooks.
type reader struct {
	bundle string
	out    *Bundle
	hooks  map[string][]content.Hook
}

// add folds one decoded surface into the bundle under construction.
//
// The type switch is exhaustive over the registered kinds and its default arm
// FAILS. A new surface type that nobody taught this function about would
// otherwise be silently dropped: the tree would enumerate it, the manifest
// would cover it, the signature would verify, and the item simply would not
// exist in the bundle anyone reads.
func (r *reader) add(ref trust.Ref, s content.Surface) error {
	switch v := s.(type) {
	case content.Fragment:
		r.addFragment(v)
	case content.Command:
		return r.addCommand(v)
	case content.MCP:
		r.addMCP(v)
	case content.Hook:
		// Buffered, not appended: a hook's place in its event's list is its
		// ORDER, and order is only resolvable once the whole event is in hand.
		r.hooks[v.Event] = append(r.hooks[v.Event], v)
	case content.Skill:
		return r.addSkill(v)
	case content.Profile:
		r.addProfile(v)
	default:
		return fmt.Errorf("bundles: tree bundle %q holds %s, a surface kind this reader does not know how to fold into a bundle document; "+
			"teach Read about it rather than letting it be dropped silently", r.bundle, ref.Key())
	}
	return nil
}

func (r *reader) addFragment(v content.Fragment) {
	put(&r.out.Fragments, v.Name, BundleFragment{ItemBody: itemBody(v.ItemMeta), Premise: v.Description})
}

// put stores one item under its name, creating the kind's map on first use
// so a kind the tree never declares stays nil, as a document bundle's would.
func put[T any](m *map[string]T, name string, v T) {
	if *m == nil {
		*m = map[string]T{}
	}
	(*m)[name] = v
}

// itemBody carries across everything a fragment and a command hold alike. It
// exists so neither reader restates the shared payload: this function dropping
// a field is one bug, whereas the two literals it replaced could disagree
// about one — which is exactly how a fragment's premise went missing while the
// command beside it kept its description.
func itemBody(m content.ItemMeta) ItemBody {
	return ItemBody{
		Tags:         m.Tags,
		Notes:        m.Notes,
		Installation: m.Installation,
		Content:      m.Body,
		ContentHash:  m.ContentHash,
		Distilled:    m.Distilled,
		DistilledBy:  m.DistilledBy,
		NoDistill:    m.NoDistill,
	}
}

func (r *reader) addCommand(v content.Command) error {
	exports, err := engineBlocks(v.Exports)
	if err != nil {
		return fmt.Errorf("bundles: command %q in tree bundle %q: %w", v.Name, r.bundle, err)
	}
	put(&r.out.Commands, v.Name, BundleCommand{ItemBody: itemBody(v.ItemMeta), Description: v.Description, Exports: exports})
	return nil
}

func (r *reader) addMCP(v content.MCP) {
	put(&r.out.MCP, v.Name, BundleMCP{
		Command:      v.Command,
		Args:         v.Args,
		Env:          v.Env,
		ServedBy:     v.ServedBy,
		Notes:        v.Notes,
		Installation: v.Installation,
		ContentHash:  v.ContentHash,
	})
}

func (r *reader) addSkill(v content.Skill) error {
	if len(v.Files) == 0 {
		return fmt.Errorf("bundles: skill %q in tree bundle %q: the package has NO files; refusing to read it as an empty skill "+
			"(an empty skill materializes without complaint and delivers nothing)", v.Name, r.bundle)
	}
	exports, err := engineBlocks(v.Exports)
	if err != nil {
		return fmt.Errorf("bundles: skill %q in tree bundle %q: %w", v.Name, r.bundle, err)
	}
	put(&r.out.Skills, v.Name, BundleSkill{
		// Path is left DEFAULT ("skills/<name>"), which is exactly where the
		// tree puts it. Writing it out explicitly would pin a layout the
		// surface type already owns, and the two could then disagree.
		Tags:    v.Tags,
		Notes:   v.Notes,
		Exports: exports,
	})
	return nil
}

func (r *reader) addProfile(v content.Profile) {
	if r.out.Profiles == nil {
		r.out.Profiles = map[string]BundleProfile{}
	}
	r.out.Profiles[v.Name] = v.Def
}

// finishHooks resolves each event's hooks into DECLARED order and appends them.
//
// This is the step a tree read cannot skip. Refs yields hooks by filename, and
// filename is identity, not sequence — the order field is the sequence, and
// content.SortHooks is the ONE rule that resolves it (the same rule the apply
// path uses). A reader that trusted the walk would emit a bundle whose hooks
// are byte-identical to a correct one and fire in the wrong order, which nothing
// downstream can detect.
func (r *reader) finishHooks() {
	for _, event := range collections.SortedKeys(r.hooks) {
		hooks := r.hooks[event]
		content.SortHooks(hooks)
		for _, h := range hooks {
			r.appendHook(event, BundleHook{
				Matcher:         h.Matcher,
				Command:         h.Command,
				Args:            h.Args,
				Type:            h.Type,
				Prompt:          h.Prompt,
				Timeout:         h.Timeout,
				Async:           h.Async,
				PreToolFallback: h.PreToolFallback,
				// Order is carried through rather than dropped: it is what made
				// this sequence resolvable, and a re-conversion that lost it
				// would fall back to positional spacing and stale every
				// countersignature on the way past.
				Order: h.Order,
			})
		}
	}
}

// appendHook puts one hook in its event's list. The switch mirrors hookEvents
// and is the write-side counterpart to BundleHooks.eventHooks; an
// unknown event is DROPPED here only because the tree walker cannot produce one
// — hooks live under hooks/<event>/ and an unrecognised directory never decodes
// to a Hook at all.
func (r *reader) appendHook(event string, h BundleHook) {
	switch event {
	case HookEventPreTool:
		r.out.Hooks.PreTool = append(r.out.Hooks.PreTool, h)
	case HookEventPostTool:
		r.out.Hooks.PostTool = append(r.out.Hooks.PostTool, h)
	case HookEventSessionStart:
		r.out.Hooks.SessionStart = append(r.out.Hooks.SessionStart, h)
	case HookEventSessionEnd:
		r.out.Hooks.SessionEnd = append(r.out.Hooks.SessionEnd, h)
	case HookEventPreShell:
		r.out.Hooks.PreShell = append(r.out.Hooks.PreShell, h)
	case HookEventPostFileEdit:
		r.out.Hooks.PostFileEdit = append(r.out.Hooks.PostFileEdit, h)
	case HookEventTurnEnd:
		r.out.Hooks.TurnEnd = append(r.out.Hooks.TurnEnd, h)
	case HookEventTurnStart:
		r.out.Hooks.TurnStart = append(r.out.Hooks.TurnStart, h)
	}
}

// engineBlocks maps the tree form's per-engine blocks onto this package's,
// block for block, each canonicalised to sorted-key JSON. Neither side reads
// inside a block, so nothing is projected and nothing is invented; an item
// declaring none yields nil.
func engineBlocks(e content.EngineExports) (EngineBlocks, error) {
	if len(e) == 0 {
		return nil, nil
	}
	out := make(EngineBlocks, len(e))
	for engine, block := range e {
		raw, err := json.Marshal(block)
		if err != nil {
			return nil, fmt.Errorf("exports: engine %q: %w", engine, err)
		}
		out[engine] = raw
	}
	return out, nil
}

// sortedTreeKeys keeps map iteration deterministic. It is a local copy rather
// than a shared helper because internal/adapters/content/convert owns the other
// direction and this package must not import it — convert imports this one.
