package bundles

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/trust"
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
		r.addCommand(v)
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
	if r.out.Fragments == nil {
		r.out.Fragments = map[string]BundleFragment{}
	}
	r.out.Fragments[v.Name] = BundleFragment{
		Tags:         v.Tags,
		Notes:        v.Notes,
		Installation: v.Installation,
		Content:      v.Body,
		ContentHash:  v.ContentHash,
		Distilled:    v.Distilled,
		DistilledBy:  v.DistilledBy,
		NoDistill:    v.NoDistill,
	}
}

func (r *reader) addCommand(v content.Command) {
	if r.out.Commands == nil {
		r.out.Commands = map[string]BundleCommand{}
	}
	r.out.Commands[v.Name] = BundleCommand{
		Description:  v.Description,
		Tags:         v.Tags,
		Notes:        v.Notes,
		Installation: v.Installation,
		Content:      v.Body,
		ContentHash:  v.ContentHash,
		Distilled:    v.Distilled,
		DistilledBy:  v.DistilledBy,
		NoDistill:    v.NoDistill,
		LLM:          commandLLM(v.Exports),
	}
}

func (r *reader) addMCP(v content.MCP) {
	if r.out.MCP == nil {
		r.out.MCP = map[string]BundleMCP{}
	}
	r.out.MCP[v.Name] = BundleMCP{
		Command:      v.Command,
		Args:         v.Args,
		Env:          v.Env,
		Notes:        v.Notes,
		Installation: v.Installation,
		ContentHash:  v.ContentHash,
	}
}

func (r *reader) addSkill(v content.Skill) error {
	if r.out.Skills == nil {
		r.out.Skills = map[string]BundleSkill{}
	}
	files, err := skillManifest(v)
	if err != nil {
		return fmt.Errorf("bundles: skill %q in tree bundle %q: %w", v.Name, r.bundle, err)
	}
	r.out.Skills[v.Name] = BundleSkill{
		// Path is left DEFAULT ("skills/<name>"), which is exactly where the
		// tree puts it. Writing it out explicitly would pin a layout the
		// surface type already owns, and the two could then disagree.
		Tags:  v.Tags,
		Notes: v.Notes,
		Files: files,
		LLM:   skillLLM(v.Exports),
	}
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
	for _, event := range sortedTreeKeys(r.hooks) {
		hooks := r.hooks[event]
		content.SortHooks(hooks)
		for _, h := range hooks {
			r.appendHook(event, BundleHook{
				Matcher:         h.Matcher,
				Command:         h.Command,
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
	}
}

// skillManifest renders a skill package's files as the generated per-file
// manifest bundle.yaml records — the shape VerifyExtractedManifest
// later checks the extracted tree against, field by field.
//
// It goes through SkillManifestEntryFor rather than hashing here: the
// verifier builds its side with the same function, so the two cannot disagree
// about the hash's "sha256:" prefix or the mode's octal width. Spelling either
// from memory produces a package that extracts, fails verification, and is
// withheld with an integrity error indistinguishable from real tampering —
// which is exactly what happened before this went through one definition.
//
// The mode comes from the DECLARED ComponentMode, never from a filesystem: that
// declaration is inside the signed bytes, and the filesystem's bit is not
// portable.
func skillManifest(s content.Skill) (map[string]SkillFileMeta, error) {
	if len(s.Files) == 0 {
		return nil, fmt.Errorf("the package has NO files; refusing to read it as an empty skill " +
			"(an empty skill materializes without complaint and delivers nothing)")
	}
	out := make(map[string]SkillFileMeta, len(s.Files))
	for _, f := range s.Files {
		e := SkillManifestEntryFor(f.Path, f.Bytes, skillFilePerm(f.Mode))
		out[e.Path] = SkillFileMeta{SHA256: e.SHA256, Mode: e.Mode}
	}
	return out, nil
}

// skillFilePerm turns a declared ComponentMode into the POSIX permission the
// package's file carries on disk. Anything not declared executable is a plain
// file — the same default the surface type applies on the way out.
func skillFilePerm(m content.ComponentMode) os.FileMode {
	if m == content.ModeExecutable {
		return 0o755
	}
	return 0o644
}

// commandLLM is the inverse of commandExports: one engine-keyed map back onto
// internal/bundles' four near-identical per-engine structs. An engine absent
// from the map is left zero; nothing is invented.
//
// It carries exactly the fields commandExports carries, no more. Codex's
// ArgumentHint is the one field bundles declares and the forward mapping drops,
// so reading it back here would invent a value the tree never held — the
// asymmetry belongs in commandExports, where the field is lost, not here.
func commandLLM(e content.EngineExports) LLMExports {
	var out LLMExports
	if x, ok := e.For("claude-code"); ok {
		out.ClaudeCode = ClaudeCodeConfig{
			Enabled: x.Enabled, Description: x.Description,
			ArgumentHint: x.ArgumentHint, AllowedTools: x.AllowedTools, Model: x.Model,
		}
	}
	if x, ok := e.For("codex"); ok {
		out.Codex = CodexConfig{Enabled: x.Enabled, Description: x.Description}
	}
	if x, ok := e.For("kiro"); ok {
		out.Kiro = KiroConfig{Enabled: x.Enabled, Description: x.Description}
	}
	if x, ok := e.For("opencode"); ok {
		out.Opencode = OpencodeConfig{Enabled: x.Enabled, Description: x.Description}
	}
	return out
}

// skillLLM is the same inverse for a skill, where only enablement is meaningful.
func skillLLM(e content.EngineExports) SkillLLMExports {
	var out SkillLLMExports
	set := func(dst *SkillEngineExport, engine string) {
		if x, ok := e.For(engine); ok {
			dst.Enabled = x.Enabled
		}
	}
	set(&out.ClaudeCode, "claude-code")
	set(&out.Codex, "codex")
	set(&out.Kiro, "kiro")
	set(&out.Opencode, "opencode")
	return out
}

// sortedTreeKeys keeps map iteration deterministic. It is a local copy rather
// than a shared helper because internal/content/convert owns the other
// direction and this package must not import it — convert imports this one.
func sortedTreeKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
