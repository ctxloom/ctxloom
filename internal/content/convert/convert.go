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
// derived names ("guard", "audit", "stamp") would produce a tree whose walk
// order is alphabetical and therefore, for most real bundles, NOT the declared
// execution order — while being byte-identical, file for file, to a tree that
// got it right. Nothing downstream could detect the difference.
//
// The design document rejected an "NN-" filename prefix as "magic filenames
// where explicit data is clearer", and separately RETRACTED the per-hook
// `order:` field it had proposed instead, on the correct grounds that nothing
// reads it: sequence is a property of merge order, which a single hook's own
// file cannot express. Both of those cannot be true at once and still preserve
// meaning. With no field to carry order and enumeration fixed to sorted-by-
// name, THE NAME IS THE ONLY CARRIER LEFT.
//
// So this package emits "hooks/<event>/<NN>-<slug>.yaml", zero-padded, and the
// ordinal is load-bearing rather than decorative. The cost is real and is
// stated rather than hidden: this reintroduces positional identity, which is
// the connascence the name-based scheme existed to remove — inserting a hook at
// the top of an event renumbers every hook below it and invalidates their
// approvals. That is a worse property than the design wanted. It is a better
// property than silently executing a user's hooks in the wrong order, which is
// the only alternative available without a format change.
//
// FLAGGED FOR A HUMAN DECISION: the alternative is to make enumeration order
// explicit somewhere the walk can read it (a per-event ordering file, say),
// which IS a format change and therefore out of scope for a converter. Until
// that is decided, Plan reports every synthesised name so the renaming is
// visible rather than discovered.
package convert

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
// package synthesised, and the ordinal prefix it had to attach to them.
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
// The returned order is the order items should be written, and for hooks it is
// also the order a directory walk will yield them back in — that equivalence is
// the whole point of the ordinal prefix and is asserted directly by the tests.
func Plan(id content.BundleID, b *bundles.Bundle, opts Options) ([]Item, error) {
	items, _, err := PlanWithReport(id, b, opts)
	return items, err
}

// PlanWithReport is Plan plus the record of what it had to synthesise.
func PlanWithReport(id content.BundleID, b *bundles.Bundle, opts Options) ([]Item, *Report, error) {
	if b == nil {
		return nil, nil, fmt.Errorf("convert: nil bundle")
	}
	bundle := string(id)
	rep := &Report{HookNames: map[string]int{}}
	var items []Item

	ref := func(kind trust.ItemKind, name string) trust.Ref {
		return trust.Ref{Bundle: bundle, Kind: kind, Name: name}
	}

	// --- fragments ---------------------------------------------------------
	for _, name := range sortedKeys(b.Fragments) {
		f := b.Fragments[name]
		surface := content.Fragment{
			Name:         name,
			Tags:         f.Tags,
			Notes:        f.Notes,
			Installation: f.Installation,
			ContentHash:  f.ContentHash,
			Body:         f.Content,
			NoDistill:    f.NoDistill,
			Distilled:    f.Distilled,
			DistilledBy:  f.DistilledBy,
		}
		items = append(items, Item{Ref: ref(trust.KindFragment, name), Form: signing.FormRaw, Surface: surface})
		// A distilled rewrite is a SEPARATE FORM of the same item, stored as a
		// sibling file. Emitting it only when it exists is what keeps
		// Item.Forms honest: a fragment that was never distilled must report
		// one form, not two with one empty.
		if f.Distilled != "" {
			items = append(items, Item{Ref: ref(trust.KindFragment, name), Form: signing.FormDistilled, Surface: surface})
		}
	}

	// --- commands ----------------------------------------------------------
	for _, name := range sortedKeys(b.Commands) {
		c := b.Commands[name]
		surface := content.Command{
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
		}
		items = append(items, Item{Ref: ref(trust.KindPrompt, name), Form: signing.FormRaw, Surface: surface})
		if c.Distilled != "" {
			items = append(items, Item{Ref: ref(trust.KindPrompt, name), Form: signing.FormDistilled, Surface: surface})
		}
	}

	// --- mcp ---------------------------------------------------------------
	for _, name := range sortedKeys(b.MCP) {
		m := b.MCP[name]
		items = append(items, Item{
			Ref:  ref(trust.KindMCP, name),
			Form: signing.FormRaw,
			Surface: content.MCP{
				Name:         name,
				Command:      m.Command,
				Args:         m.Args,
				Env:          m.Env,
				Notes:        m.Notes,
				Installation: m.Installation,
				ContentHash:  m.ContentHash,
			},
		})
	}

	// --- hooks -------------------------------------------------------------
	for _, ev := range hookEvents(b.Hooks) {
		width := ordinalWidth(len(ev.Hooks))
		for i, h := range ev.Hooks {
			name := hookName(i, width, h)
			rep.HookNames[ev.Event+"/"+name] = i
			items = append(items, Item{
				Ref:  ref(trust.KindHook, ev.Event+"/"+name),
				Form: signing.FormRaw,
				Surface: content.Hook{
					Event:           ev.Event,
					Name:            name,
					Matcher:         h.Matcher,
					Type:            h.Type,
					Command:         h.Command,
					Prompt:          h.Prompt,
					Timeout:         h.Timeout,
					Async:           h.Async,
					PreToolFallback: h.PreToolFallback,
				},
			})
		}
	}

	// --- skills ------------------------------------------------------------
	for _, name := range sortedKeys(b.Skills) {
		s := b.Skills[name]
		if opts.SkillFiles == nil {
			return nil, nil, fmt.Errorf("convert: bundle %q declares skill %q but no SkillFiles reader was supplied, "+
				"so its package files cannot be read; converting it would write an EMPTY skill package", bundle, name)
		}
		files, err := opts.SkillFiles(name)
		if err != nil {
			return nil, nil, fmt.Errorf("convert: read skill %q package files: %w", name, err)
		}
		if len(files) == 0 {
			return nil, nil, fmt.Errorf("convert: skill %q has NO package files; refusing to convert it to an empty package "+
				"(an empty skill converts, signs and materializes without complaint and delivers nothing)", name)
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		items = append(items, Item{
			Ref:  ref(trust.KindSkill, name),
			Form: signing.FormRaw,
			Surface: content.Skill{
				Name:    name,
				Tags:    s.Tags,
				Notes:   s.Notes,
				Exports: skillExports(s.LLM),
				Files:   files,
			},
		})
	}

	// --- profiles ----------------------------------------------------------
	for _, name := range sortedKeys(b.Profiles) {
		items = append(items, Item{
			Ref:     ref(content.KindProfile, name),
			Form:    signing.FormRaw,
			Surface: content.Profile{Name: name, Def: b.Profiles[name]},
		})
	}

	return items, rep, nil
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

// Convert is Plan followed by Apply.
func Convert(ctx context.Context, w content.Writer, id content.BundleID, b *bundles.Bundle, opts Options) error {
	items, err := Plan(id, b, opts)
	if err != nil {
		return err
	}
	return Apply(ctx, w, items)
}

// hookName synthesises a hook's filename from its ordinal and its own content.
//
// The ORDINAL leads, zero-padded, because sorted-by-name is how the tree
// enumerates and the ordinal is the only thing that can make that order match
// the declared one (see the package doc). The slug follows so the file is
// still identifiable by a human reading a directory listing rather than being a
// bare number.
//
// A synthesised name IS positional identity under a different spelling, and
// this is the honest place to say so: today's hooks have NO name field at all
// (bundles.BundleHook has none), so there is nothing else to derive identity
// from. Naming it after its position is not a choice this converter made; it is
// the only information the source document carries.
func hookName(index, width int, h bundles.BundleHook) string {
	slug := slugify(firstNonEmpty(h.Matcher, h.Command, h.Prompt, h.Type))
	if slug == "" {
		slug = "hook"
	}
	return fmt.Sprintf("%0*d-%s", width, index, slug)
}

// ordinalWidth returns how many digits the ordinals need so that STRING
// comparison of the padded numbers matches NUMERIC order. Without this, "10-x"
// sorts before "2-x" and an event with ten or more hooks silently reorders —
// the exact bug the ordinal exists to prevent, reintroduced by the ordinal.
func ordinalWidth(n int) int {
	w := 1
	for max := 10; max <= n-1; max *= 10 {
		w++
	}
	return w
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

// commandExports maps internal/bundles' five near-identical per-engine structs
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
	add("antigravity", content.EngineExport{Enabled: l.Antigravity.Enabled, Description: l.Antigravity.Description})
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
	add("antigravity", l.Antigravity)
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
