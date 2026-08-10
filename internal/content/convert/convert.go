// Package convert turns a monolithic bundle document into the on-disk tree the
// content package reads.
//
// It is a CONVERTER, not a dual-format reader. Nothing here is consulted at
// read time and nothing here makes the tree store able to fall back to a
// bundle.yaml: it is a one-way migration, run once per bundle, and the old
// document is expected to go away afterwards. A compatibility shim would mean
// carrying both formats forever, and re-init is this project's documented
// upgrade path.
//
// # Why this package rather than internal/content
//
// The converter is the ONE place that has to understand both shapes at once, so
// it is the one place that imports both. Putting it inside internal/content
// would point that dependency the wrong way — bundles is meant to consume the
// content package eventually, not the reverse — and would become an import
// cycle the moment it did. Keeping it in a leaf subpackage means the cycle
// cannot form.
//
// # Plan and Apply are separate on purpose
//
// Plan is PURE: a bundle document in, an ordered slice of items out, no
// filesystem and no writes. That is what makes every per-kind conversion rule
// testable without staging a tree, and it is what lets a caller inspect what a
// conversion WOULD do — which matters here, because one of those rules
// synthesises names (see the hook section) and a silent rename is exactly the
// kind of thing that should be inspectable before it is committed.
//
// # The hook-ordering problem, and the decision this package makes
//
// This is the one genuinely hard case, and it is worth reading before changing
// anything here.
//
// In a bundle document a hook event is an ORDERED LIST ([]bundles.BundleHook),
// and within an event hooks merge by PURE APPEND across every source —
// nothing sorts them, nothing consults a per-hook ordering field. So a hook's
// position in that list IS its execution order.
//
// In the tree, hooks are one file per hook at "hooks/<event>/<name>.yaml", and
// an item enumeration is a DIRECTORY WALK, which yields entries sorted by
// filename. Those two facts do not agree. A converter that emitted content-
// derived names ("guard", "audit", "stamp") and left it there would produce a
// tree whose walk order is alphabetical and therefore, for most real bundles,
// NOT the declared execution order — while being byte-identical, file for file,
// to a tree that got it right. Nothing downstream could detect the difference.
//
// An earlier revision of this package solved that with an "NN-" filename prefix,
// zero-padded, because with no field to carry order and enumeration fixed to
// sorted-by-name, the name was the only carrier left. That prefix is GONE. It
// worked, and its cost was the reason to remove it: it made identity POSITIONAL,
// so inserting a hook at the top of an event renamed every hook below it,
// changing bytes and staling countersignatures for items that had not changed —
// the exact connascence the name-based scheme existed to delete.
//
// Order is now DATA: content.Hook.Order, a sparse integer in the hook's metadata
// sidecar, resolved by content.SortHooks. The field is not a decoration nobody
// reads (the failure mode that got its first draft retracted) — SortHooks is how
// a tree's hooks resolve at all, config.extractHooksFromBundle applies the same
// rule on the apply path, and `ctxloom manage hooks list` reports it.
//
// So this package emits "hooks/<event>/<slug>.yaml" for identity and writes the
// declared sequence into the order field, spaced by content.HookOrderStep. Plan
// still reports every synthesised name so the renaming is visible rather than
// discovered.
package convert

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Item is one planned tree item: where it goes, which form it is, and the
// decoded surface whose codec will produce its bytes.
//
// The surface is carried rather than pre-encoded bytes so that conversion
// reuses the SAME codec the reader uses. Hand-rolling the YAML here would be a
// second encoder to drift out of step with the first, and the drift would show
// up as a signature that stops verifying rather than as a test failure.
type Item struct {
	Ref     trust.Ref
	Form    signing.Form
	Surface content.Surface
}

// Options carries what a bundle document cannot supply on its own.
type Options struct {
	// SkillFiles reads one skill package's files. It is a function rather than
	// a filesystem because a skill's bytes can come from an authored directory,
	// an archive or an embedded FS, and the converter should not care which.
	//
	// It is REQUIRED when the bundle declares any skill. A bundle that declares
	// a skill and cannot supply its files is a conversion that would produce an
	// empty package — which converts, signs and materializes perfectly happily
	// while giving the model nothing — so it is refused instead.
	SkillFiles func(name string) ([]content.SkillFile, error)
}

// Report describes what a conversion did that a caller could not have predicted
// from the input alone. Today that is exactly one thing: the hook names this
// package synthesised from hook content.
type Report struct {
	// HookNames maps "<event>/<synthesised-name>" to the hook's ZERO-BASED
	// declared position within its event, so a caller can show the renaming.
	HookNames map[string]int
}

// hookEvents pairs each event's wire name with its list, in a FIXED order.
//
// It is a slice rather than a map so conversion output is deterministic, and it
// is exhaustive over bundles.BundleHooks by construction — a seventh event
// added to that struct and not added here is caught by
// TestPlan_EverySixHookEventsConverts rather than by a user noticing their
// hooks stopped firing.
func hookEvents(h bundles.BundleHooks) []struct {
	Event string
	Hooks []bundles.BundleHook
} {
	return []struct {
		Event string
		Hooks []bundles.BundleHook
	}{
		{"pre_tool", h.PreTool},
		{"post_tool", h.PostTool},
		{"session_start", h.SessionStart},
		{"session_end", h.SessionEnd},
		{"pre_shell", h.PreShell},
		{"post_file_edit", h.PostFileEdit},
	}
}

// Plan converts a bundle document into the ordered items it becomes, without
// touching a filesystem.
//
// The returned order is the order items should be WRITTEN. For hooks it is also
// their declared execution sequence, which a directory walk will NOT reproduce —
// the walk yields them by name. Resolving a walk's hooks back into this sequence
// is content.SortHooks' job, and both directions are asserted by the tests.
func Plan(id content.BundleID, b *bundles.Bundle, opts Options) ([]Item, error) {
	items, _, err := PlanWithReport(id, b, opts)
	return items, err
}

// PlanWithReport is Plan plus the record of what it had to synthesise.
func PlanWithReport(id content.BundleID, b *bundles.Bundle, opts Options) ([]Item, *Report, error) {
	if b == nil {
		return nil, nil, fmt.Errorf("convert: nil bundle")
	}
	p := &planner{bundle: string(id), rep: &Report{HookNames: map[string]int{}}}
	// Kind order is fixed so a conversion is reproducible from the same
	// document.
	p.fragments(b)
	p.commands(b)
	p.mcp(b)
	p.hooks(b)
	if err := p.skills(b, opts); err != nil {
		return nil, nil, err
	}
	p.profiles(b)
	return p.items, p.rep, nil
}

// planner accumulates one conversion. The per-kind methods are separate rather
// than one long function so each kind's rule is readable on its own — and so
// adding a kind means adding a method rather than growing a branch.
type planner struct {
	bundle string
	rep    *Report
	items  []Item
}

func (p *planner) add(kind trust.ItemKind, name string, form signing.Form, s content.Surface) {
	p.items = append(p.items, Item{
		Ref:     trust.Ref{Bundle: p.bundle, Kind: kind, Name: name},
		Form:    form,
		Surface: s,
	})
}

// addForms emits the raw form always and the distilled form only when a
// distilled rewrite exists. Emitting it unconditionally would make Item.Forms
// claim a form whose file is empty; omitting it when one exists would silently
// downgrade every consumer on distilled context back to the verbose original.
func (p *planner) addForms(kind trust.ItemKind, name, distilled string, s content.Surface) {
	p.add(kind, name, signing.FormRaw, s)
	if distilled != "" {
		p.add(kind, name, signing.FormDistilled, s)
	}
}

func (p *planner) fragments(b *bundles.Bundle) {
	for _, name := range sortedKeys(b.Fragments) {
		f := b.Fragments[name]
		p.addForms(trust.KindFragment, name, f.Distilled, content.Fragment{
			Name:         name,
			Tags:         f.Tags,
			Notes:        f.Notes,
			Installation: f.Installation,
			ContentHash:  f.ContentHash,
			Body:         f.Content,
			NoDistill:    f.NoDistill,
			Distilled:    f.Distilled,
			DistilledBy:  f.DistilledBy,
		})
	}
}

func (p *planner) commands(b *bundles.Bundle) {
	for _, name := range sortedKeys(b.Commands) {
		c := b.Commands[name]
		p.addForms(trust.KindPrompt, name, c.Distilled, content.Command{
			Name:         name,
			Description:  c.Description,
			Tags:         c.Tags,
			Notes:        c.Notes,
			Installation: c.Installation,
			ContentHash:  c.ContentHash,
			Body:         c.Content,
			NoDistill:    c.NoDistill,
			Distilled:    c.Distilled,
			DistilledBy:  c.DistilledBy,
			Exports:      commandExports(c.LLM),
		})
	}
}

func (p *planner) mcp(b *bundles.Bundle) {
	for _, name := range sortedKeys(b.MCP) {
		m := b.MCP[name]
		p.add(trust.KindMCP, name, signing.FormRaw, content.MCP{
			Name:         name,
			Command:      m.Command,
			Args:         m.Args,
			Env:          m.Env,
			Notes:        m.Notes,
			Installation: m.Installation,
			ContentHash:  m.ContentHash,
		})
	}
}

func (p *planner) hooks(b *bundles.Bundle) {
	for _, ev := range hookEvents(b.Hooks) {
		used := map[string]bool{}
		for i, h := range ev.Hooks {
			name := uniqueHookName(used, h)
			p.rep.HookNames[ev.Event+"/"+name] = i
			p.add(trust.KindHook, ev.Event+"/"+name, signing.FormRaw, content.Hook{
				Event:           ev.Event,
				Name:            name,
				Order:           hookOrder(i, h),
				Matcher:         h.Matcher,
				Type:            h.Type,
				Command:         h.Command,
				Prompt:          h.Prompt,
				Timeout:         h.Timeout,
				Async:           h.Async,
				PreToolFallback: h.PreToolFallback,
			})
		}
	}
}

// hookOrder returns the order value to record for a hook at declared position i.
//
// An AUTHORED order wins outright. It is the only statement of intent the source
// document carries about sequence beyond position, and overwriting it would
// discard the more specific fact in favour of the less specific one.
//
// Otherwise the position becomes the order, spaced by content.HookOrderStep. That
// spacing is not decoration: assigning 0,1,2 would mean the very first hook
// inserted afterwards renumbers the tail, which is the problem this field exists
// to solve, arriving one edit later.
func hookOrder(i int, h bundles.BundleHook) *int {
	if h.Order != nil {
		v := *h.Order
		return &v
	}
	v := (i + 1) * content.HookOrderStep
	return &v
}

func (p *planner) skills(b *bundles.Bundle, opts Options) error {
	for _, name := range sortedKeys(b.Skills) {
		s := b.Skills[name]
		files, err := readSkillFiles(p.bundle, name, opts)
		if err != nil {
			return err
		}
		p.add(trust.KindSkill, name, signing.FormRaw, content.Skill{
			Name:    name,
			Tags:    s.Tags,
			Notes:   s.Notes,
			Exports: skillExports(s.LLM),
			Files:   files,
		})
	}
	return nil
}

// readSkillFiles reads one skill package, refusing every route to an EMPTY
// package. An empty skill converts, signs and materializes without complaint
// and delivers the model nothing, so both "no reader supplied" and "the reader
// returned nothing" are hard errors rather than an empty Files slice.
func readSkillFiles(bundle, name string, opts Options) ([]content.SkillFile, error) {
	if opts.SkillFiles == nil {
		return nil, fmt.Errorf("convert: bundle %q declares skill %q but no SkillFiles reader was supplied, "+
			"so its package files cannot be read; converting it would write an EMPTY skill package", bundle, name)
	}
	files, err := opts.SkillFiles(name)
	if err != nil {
		return nil, fmt.Errorf("convert: read skill %q package files: %w", name, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("convert: skill %q has NO package files; refusing to convert it to an empty package "+
			"(an empty skill converts, signs and materializes without complaint and delivers nothing)", name)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (p *planner) profiles(b *bundles.Bundle) {
	for _, name := range sortedKeys(b.Profiles) {
		p.add(content.KindProfile, name, signing.FormRaw, content.Profile{Name: name, Def: b.Profiles[name]})
	}
}

// Apply writes planned items through a content.Writer.
//
// It stops at the first failure rather than continuing, because a partially
// converted tree is worse than an unconverted one: it looks like a tree, it
// enumerates, and it is missing items nobody asked it about.
func Apply(ctx context.Context, w content.Writer, items []Item) error {
	for _, it := range items {
		if err := w.Put(ctx, it.Ref, it.Form, it.Surface); err != nil {
			return fmt.Errorf("convert: write %s (%s): %w", it.Ref.Key(), it.Form, err)
		}
	}
	return nil
}

// Convert is Plan, then Apply, then the ENVELOPE.
//
// The envelope is not optional and not an afterthought: bundles.ReadTree
// requires it, because version/description/tags/author have no other carrier —
// no item can supply them. A conversion that wrote only items produced a tree
// its own reader refuses, which is what this function used to do.
func Convert(ctx context.Context, w content.Writer, id content.BundleID, b *bundles.Bundle, opts Options) error {
	items, err := Plan(id, b, opts)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		// An empty bundle stays a no-op. Writing an envelope alone would create a
		// tree with no items, which bundles.ReadTree refuses anyway — so it would
		// be a directory that exists solely to be unreadable.
		return nil
	}
	if err := Apply(ctx, w, items); err != nil {
		return err
	}
	return writeEnvelope(ctx, w, id, b)
}

// writeEnvelope renders the bundle-level document a tree carries at
// bundle.yaml: the source bundle with every ITEM map cleared, so the tree has
// exactly one answer for each item — the file — and the envelope carries only
// what is genuinely bundle-level.
//
// Clearing rather than hand-copying the six metadata fields is deliberate: a
// field added to bundles.Bundle travels automatically, whereas a copy list is a
// second place to forget it, and a forgotten field is silently dropped on
// migration.
func writeEnvelope(ctx context.Context, w content.Writer, id content.BundleID, b *bundles.Bundle) error {
	env := *b
	env.Fragments = nil
	env.Commands = nil
	env.MCP = nil
	env.Skills = nil
	env.Profiles = nil
	env.Hooks = bundles.BundleHooks{}
	raw, err := yaml.Marshal(&env)
	if err != nil {
		return fmt.Errorf("convert: rendering the %s envelope for %q: %w", bundles.DirectoryFormManifest, id, err)
	}
	if err := w.PutRootFile(ctx, id, bundles.DirectoryFormManifest, raw); err != nil {
		return fmt.Errorf("convert: writing the envelope for %q: %w", id, err)
	}
	return nil
}

// uniqueHookName synthesises a hook's filename from its CONTENT, and only from
// its content: the name is identity, never sequence. Sequence is the order field.
//
// Today's bundle hooks have no name of their own (bundles.BundleHook has no such
// field), so a name has to be derived, and content is the only honest source —
// deriving it from position is what the retired `<NN>-` prefix did, and that is
// precisely the positional identity being removed.
//
// Content-derived names COLLIDE, which the ordinal used to hide. Two hooks
// running the same command produce the same slug, and a collision here means one
// hook silently overwrites the other: it converts, signs and materializes without
// complaint and is simply not there. So collisions take a "-2", "-3" suffix in
// declaration order. That is still weakly positional among IDENTICAL hooks, and
// it is the narrowest place to put the residue — two byte-identical hooks are
// interchangeable, so which one keeps the bare name cannot matter.
func uniqueHookName(used map[string]bool, h bundles.BundleHook) string {
	slug := slugify(firstNonEmpty(h.Matcher, h.Command, h.Prompt, h.Type))
	if slug == "" {
		slug = "hook"
	}
	name := slug
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s-%d", slug, n)
	}
	used[name] = true
	return name
}

// slugify reduces arbitrary text to a single safe path segment. It is
// deliberately lossy — it names a file, it does not carry meaning; the hook's
// actual configuration is inside the file.
func slugify(s string) string {
	var b strings.Builder
	lastDash := true // leading dashes suppressed
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// commandExports maps internal/bundles' four near-identical per-engine structs
// onto the content package's one engine-keyed map. Engines absent from the
// source struct simply do not appear; nothing is invented.
func commandExports(l bundles.LLMExports) content.EngineExports {
	out := content.EngineExports{}
	add := func(engine string, e content.EngineExport) {
		if e.Enabled == nil && e.Description == "" && e.ArgumentHint == "" && e.Model == "" && len(e.AllowedTools) == 0 {
			return
		}
		out[engine] = e
	}
	add("claude-code", content.EngineExport{
		Enabled: l.ClaudeCode.Enabled, Description: l.ClaudeCode.Description,
		ArgumentHint: l.ClaudeCode.ArgumentHint, AllowedTools: l.ClaudeCode.AllowedTools, Model: l.ClaudeCode.Model,
	})
	add("codex", content.EngineExport{Enabled: l.Codex.Enabled, Description: l.Codex.Description})
	add("kiro", content.EngineExport{Enabled: l.Kiro.Enabled, Description: l.Kiro.Description})
	add("opencode", content.EngineExport{Enabled: l.Opencode.Enabled, Description: l.Opencode.Description})
	if len(out) == 0 {
		return nil
	}
	return out
}

// skillExports is the same mapping for a skill, where only enablement is
// meaningful (a skill's name and description are SKILL.md front-matter, the
// single source of truth, and are never duplicated into the sidecar).
func skillExports(l bundles.SkillLLMExports) content.EngineExports {
	out := content.EngineExports{}
	add := func(engine string, e bundles.SkillEngineExport) {
		if e.Enabled == nil {
			return
		}
		out[engine] = content.EngineExport{Enabled: e.Enabled}
	}
	add("claude-code", l.ClaudeCode)
	add("codex", l.Codex)
	add("kiro", l.Kiro)
	add("opencode", l.Opencode)
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
