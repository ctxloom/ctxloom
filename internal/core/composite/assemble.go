package composite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// contextSectionSeparator joins the sections of an assembled context. It is
// the same separator the context-file writer splits on, so a context
// assembled here and one written to a file split on the same boundaries.
const contextSectionSeparator = "\n\n---\n\n"

// Assemble is the ONE constructor from sources. It reads nothing: cat is
// resolved, profiles loaded, trust built. It refuses an ungated trust and a
// withheld required item (unless Options.DropWithheld, recorded in the
// attestation).
//
// The context: every fragment in selection order, loaded through the gated
// process stage, the profile variables substituted, a premised fragment
// held back for the catalog unless the caller named it or the assembly is
// static, then the builtin injections; the same item reaching the context
// twice is assembled once. Commands and skills: the injected ones, then the
// curated asks (marked Curated) or, uncurated, every one the selection's
// bundles and the catalog's companion loadouts ship, one per item. Hooks,
// MCP servers, the deny list and the statusline are carried as the caller
// resolved them.
func Assemble(ctx context.Context, cat bundles.Catalog, sel Selection, tr Trust, opts Options) (Package, error) {
	if !tr.Gates() {
		return Package{}, ErrUngatedAssembly
	}
	pipe := opts.Pipeline
	if pipe == nil {
		loader := bundles.LoaderOf(cat)
		if opts.Versions != nil {
			loader.WithVersionResolver(opts.Versions)
		}
		pipe = bundles.NewPipeline(loader, tr.Authorizer(), linkGrant(opts.MCP), opts.PreferDistilled)
	}
	a := &assembly{sel: sel, opts: opts, pipe: pipe, ingest: newIngest()}

	a.fragments()
	a.commands()
	a.skills()

	withheld := pipe.Withheld()
	if len(withheld) > 0 && !opts.DropWithheld {
		return Package{}, fmt.Errorf("%w: %s", ErrItemWithheld, strings.Join(withheld, ", "))
	}
	text := a.ingest.join()
	pkg := Package{
		Context:    Context{Text: text, Hash: digest([]byte(text))},
		Fragments:  a.delivered,
		Premised:   a.premised,
		Commands:   a.commandItems,
		Skills:     a.skillItems,
		Hooks:      opts.Hooks,
		MCP:        maps.Clone(opts.MCP),
		Links:      linkGroups(opts.MCP),
		DenyTools:  slices.Clone(opts.DenyTools),
		Statusline: opts.Statusline,
		Selection:  sel,
		Loaded:     a.loaded,
		Findings:   a.findings,
		attestation: Attestation{
			Items:    a.rows,
			Withheld: withheld,
		},
	}
	return pkg, nil
}

// assembly is the state of one Assemble call.
type assembly struct {
	sel      Selection
	opts     Options
	pipe     *bundles.Pipeline
	ingest   *ingest
	explicit map[string]bool

	delivered    []Item[Fragment]
	premised     []Item[Fragment]
	loaded       []string
	commandItems []Item[Command]
	skillItems   []Item[Skill]
	rows         []ItemAttestation
	findings     []Finding
}

// fragments loads the selection order, then the builtin injections.
func (a *assembly) fragments() {
	a.explicit = make(map[string]bool, len(a.sel.Explicit))
	for _, name := range a.sel.Explicit {
		a.explicit[name] = true
	}
	for _, ask := range a.sel.Fragments {
		lc, err := a.load(ask)
		if err != nil {
			// A withheld fragment is not a load failure: the gate recorded
			// it, and the attestation names it.
			if !errors.Is(err, errs.ErrFragmentWithheld) {
				a.findings = append(a.findings, Finding{Kind: FindingLoadFailed, Ref: ask.Name, Version: ask.Version, Message: err.Error()})
			}
			continue
		}
		item := Item[Fragment]{
			Value:    Fragment{Name: ask.Name, Premise: lc.Premise},
			Ref:      ask.Name,
			Form:     lc.Form,
			Decision: trust.Allow,
			Signer:   lc.Signer,
		}
		if a.holdBack(lc.Premise, ask.Name) {
			item.Value.Body = a.substitute(ask.Name, lc.Content)
			a.premised = append(a.premised, item)
			a.row(ask.Name, item.Value.Body)
			continue
		}
		item.Value.Body = a.substitute(ask.Name, lc.Content)
		a.deliver(item, lc.TrustRef)
	}
	for _, f := range a.opts.Builtin {
		body := strings.TrimSpace(f.Body)
		item := Item[Fragment]{Value: Fragment{Name: f.Name, Body: body, Premise: f.Premise}, Ref: f.Name, Form: bundles.FormRaw, Decision: trust.Allow}
		if a.holdBack(f.Premise, f.Name) {
			a.premised = append(a.premised, item)
			a.row(f.Name, body)
			continue
		}
		a.deliver(item, f.Name)
	}
}

// load resolves one ask through the process stage, honouring a pinned
// content version.
func (a *assembly) load(ask FragmentAsk) (*bundles.LoadedContent, error) {
	if ask.Version == "" {
		return a.pipe.GetFragment(ask.Name)
	}
	return a.pipe.GetFragmentAtVersion(ask.Name, ask.Version)
}

// holdBack is THE premise rule: a fragment carrying a premise is held back
// from unconditional assembly unless the caller named it (naming it is the
// selection) or the assembly is static (nothing behind the surface can
// pull it later, so holding it back would lose it). A fragment with no
// premise is always loaded — absence asserts it applies unconditionally.
func (a *assembly) holdBack(premise, name string) bool {
	return premise != "" && !a.opts.Static && !a.explicit[name]
}

// substitute applies the profile variables to one fragment, reporting each
// undefined variable or template fault as a finding naming the fragment.
func (a *assembly) substitute(name, content string) string {
	return substituteVariables(strings.TrimSpace(content), a.sel.Variables, func(msg string) {
		a.findings = append(a.findings, Finding{Kind: FindingSubstitution, Ref: name, Message: msg})
	})
}

// deliver ingests a fragment into the context — once per item — and records
// its attestation row. identity is the ref the item's identity key derives
// from (the read's trust ref for a loaded fragment, the injection's own name
// for a builtin).
func (a *assembly) deliver(item Item[Fragment], identity string) {
	a.loaded = append(a.loaded, item.Ref)
	kept, dup := a.ingest.add(identityKey(identity), item.Value.Body, item.Ref)
	if dup {
		if kept != item.Ref {
			a.findings = append(a.findings, Finding{Kind: FindingDuplicate, Ref: item.Ref,
				Message: fmt.Sprintf("the same fragment reached this context twice: %q is already assembled and %q is the same item with identical content, so the second copy was dropped — if these were meant to be two DIFFERENT fragments, one of the two references is wrong", kept, item.Ref)})
		}
		return
	}
	a.delivered = append(a.delivered, item)
	a.row(item.Ref, item.Value.Body)
}

func (a *assembly) row(ref, body string) {
	a.rows = append(a.rows, ItemAttestation{Ref: ref, Decision: trust.Allow, Hash: digest([]byte(body))})
}

// commands: the injected ones, then the curated asks or the bundles' set.
func (a *assembly) commands() {
	seen := map[string]bool{}
	add := func(c Command, ref, signer string, form bundles.ContentForm) {
		if c.Item != "" && seen[c.Item] {
			return
		}
		if c.Item != "" {
			seen[c.Item] = true
		}
		a.commandItems = append(a.commandItems, Item[Command]{Value: c, Ref: ref, Form: form, Decision: trust.Allow, Signer: signer})
		a.row(ref, c.Body)
	}
	for _, c := range a.opts.Commands {
		add(c, c.Name, "", bundles.FormRaw)
	}
	fromLoaded := func(lc *bundles.LoadedContent, curated bool) {
		add(Command{
			Name:        lc.Name,
			Bundle:      lc.Bundle,
			Item:        lc.Item,
			Tags:        slices.Clone(lc.Tags),
			Description: lc.Description,
			Body:        lc.Content,
			Exports:     blocks(lc.Exports),
			Curated:     curated,
		}, lc.TrustRef, lc.Signer, lc.Form)
	}
	if len(a.sel.Commands) > 0 {
		for _, ask := range a.sel.Commands {
			name, version, err := bundles.SplitCommandVersion(ask.Ref)
			var lc *bundles.LoadedContent
			if err == nil {
				if version == "" {
					lc, err = a.pipe.GetCommand(ask.Ref)
				} else {
					lc, err = a.pipe.GetPromptAtVersion(name, version)
				}
			}
			if err != nil {
				a.findings = append(a.findings, Finding{Kind: FindingCuratedSkipped, Ref: ask.Ref, Message: err.Error()})
				continue
			}
			fromLoaded(lc, true)
		}
		// A companion's commands are unconditional whenever the companion
		// is present; a curation names bundle commands, never theirs.
		for _, ref := range companionRefs(a.pipe.Loader().Catalog()) {
			for _, lc := range a.pipe.CommandsFromBundleRef(ref) {
				fromLoaded(lc, false)
			}
		}
		return
	}
	for _, ref := range a.sel.Bundles {
		for _, lc := range a.pipe.CommandsFromBundleRef(ref) {
			fromLoaded(lc, false)
		}
	}
	for _, ref := range companionRefs(a.pipe.Loader().Catalog()) {
		for _, lc := range a.pipe.CommandsFromBundleRef(ref) {
			fromLoaded(lc, false)
		}
	}
}

// skills: the curated asks or the bundles' set, one per item.
func (a *assembly) skills() {
	seen := map[string]bool{}
	add := func(ls *bundles.LoadedSkill, curated bool) {
		if seen[ls.Item] {
			return
		}
		seen[ls.Item] = true
		files := make([]engine.SkillFile, 0, len(ls.Files))
		h := sha256.New()
		for _, f := range ls.Files {
			files = append(files, engine.SkillFile{Path: f.RelPath, Digest: digest(f.Content), Size: int64(len(f.Content)), Mode: f.Mode, Bytes: f.Content})
			fmt.Fprintf(h, "%d:%s%d:", len(f.RelPath), f.RelPath, len(f.Content))
			h.Write(f.Content)
		}
		a.skillItems = append(a.skillItems, Item[Skill]{
			Value: Skill{
				Name:        ls.Frontmatter.Name,
				Bundle:      ls.Bundle,
				Item:        ls.Item,
				Tags:        slices.Clone(ls.Tags),
				Description: ls.Frontmatter.Description,
				Files:       files,
				Exports:     blocks(ls.Exports),
				Curated:     curated,
			},
			Ref: ls.TrustRef, Form: bundles.FormRaw, Decision: trust.Allow, Signer: ls.Signer,
		})
		a.rows = append(a.rows, ItemAttestation{Ref: ls.TrustRef, Decision: trust.Allow, Hash: hex.EncodeToString(h.Sum(nil))})
	}
	if len(a.sel.Skills) > 0 {
		for _, ask := range a.sel.Skills {
			ls, err := a.pipe.GetSkill(ask.Ref)
			if err != nil {
				a.findings = append(a.findings, Finding{Kind: FindingCuratedSkipped, Ref: ask.Ref, Message: err.Error()})
				continue
			}
			add(ls, true)
		}
		return
	}
	for _, ref := range a.sel.Bundles {
		for _, ls := range a.pipe.SkillsFromBundleRef(ref) {
			add(ls, false)
		}
	}
}

// blocks copies the opaque per-engine blocks onto the package's own type.
func blocks(e bundles.EngineBlocks) map[string][]byte {
	if len(e) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(e))
	for engine, raw := range e {
		out[engine] = slices.Clone(raw)
	}
	return out
}

// companionRefs are the catalog's companion loadout refs, in name order:
// what was READ, not a second discovery pass.
func companionRefs(cat bundles.Catalog) []string {
	reads := cat.Scoped(bundles.ProvenanceCompanion).Reads()
	out := make([]string, 0, len(reads))
	for _, read := range reads {
		out = append(out, read.DisplayName())
	}
	return out
}

// linkGrant answers the link-group question from the run's OWN granted
// set — the servers the caller resolved for the same profiles the engine
// is launched with — keyed by server name AND owning bundle, so a same-named
// server from another bundle cannot stand in for the one an item depends on.
func linkGrant(mcp map[string]wire.MCPServer) bundles.LinkGrant {
	return bundles.LinkGrantFunc(func(read bundles.BundleRead, server string) bool {
		srv, ok := mcp[server]
		return ok && srv.SCM == bundles.BundleSCM(read.SourceRef())
	})
}

// linkGroups names the servers the package grants, in name order.
func linkGroups(mcp map[string]wire.MCPServer) []LinkGroup {
	out := make([]LinkGroup, 0, len(mcp))
	for _, name := range slices.Sorted(maps.Keys(mcp)) {
		out = append(out, LinkGroup{Server: name})
	}
	return out
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// identityKey reduces an item ref to the source-agnostic identity the
// context dedupes on — trust.Ref.Key(), "<bundle>#<kind>/<name>" — so a
// project bundle that shadows a builtin of the same name and the builtin's
// own injection dedupe to ONE occurrence even though they carry two trust
// identities. A ref outside the canonical grammar is used verbatim: it can
// then only match another occurrence spelled the same way, never a
// different one.
func identityKey(ref string) string {
	if br, err := trust.ParseBundleRef(ref); err == nil {
		return trust.RefFromBundleRef(br).Key()
	}
	return ref
}

// ingest IS the ingest layer for the context: every fragment passes
// through add, whichever route brought it, and it alone decides whether an
// arriving fragment is new content or a second copy of content already
// ingested. Two fragments are the same content — and the second is dropped
// — when they name the SAME item (source-agnostic) AND their bytes are
// identical ignoring surrounding whitespace. The FIRST occurrence is kept;
// nothing here reorders.
type ingest struct {
	parts []string
	seen  map[ingestIdentity]string
}

type ingestIdentity struct{ item, content string }

func newIngest() *ingest { return &ingest{seen: map[ingestIdentity]string{}} }

// add ingests one fragment, reporting the ref that was kept and whether
// this one was dropped as a duplicate of it.
func (in *ingest) add(item, content, ref string) (kept string, dup bool) {
	id := ingestIdentity{item: item, content: strings.TrimSpace(content)}
	if kept, dup := in.seen[id]; dup {
		return kept, true
	}
	in.seen[id] = ref
	in.parts = append(in.parts, content)
	return ref, false
}

// join renders the ingested fragments as the context string, blank
// sections omitted.
func (in *ingest) join() string {
	parts := make([]string, 0, len(in.parts))
	for _, p := range in.parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, contextSectionSeparator)
}

// EngineItems is the engine-facing projection an Engine.Exports decides
// over: every fragment (a premised one carries its premise), every command
// and skill with THIS engine's opaque block and nothing of any other
// engine's, the hooks and servers, and whether settings are present. It is
// how composite hands content to an engine without an engine package ever
// importing composite.
func (p Package) EngineItems(name engine.Name) engine.Items {
	items := engine.Items{Settings: len(p.DenyTools) > 0 || p.Statusline}
	for _, f := range p.Fragments {
		items.Fragments = append(items.Fragments, engine.FragmentItem{Ref: f.Ref, Name: f.Value.Name, Body: []byte(f.Value.Body), Premise: f.Value.Premise})
	}
	for _, f := range p.Premised {
		items.Fragments = append(items.Fragments, engine.FragmentItem{Ref: f.Ref, Name: f.Value.Name, Body: []byte(f.Value.Body), Premise: f.Value.Premise})
	}
	for _, c := range p.Commands {
		items.Commands = append(items.Commands, engine.CommandItem{Ref: c.Ref, Name: c.Value.Name, Description: c.Value.Description, Body: []byte(c.Value.Body), Exports: block(c.Value.Exports, name), Curated: c.Value.Curated})
	}
	for _, s := range p.Skills {
		items.Skills = append(items.Skills, engine.SkillItem{Ref: s.Ref, Name: s.Value.Name, Description: s.Value.Description, Files: s.Value.Files, Exports: block(s.Value.Exports, name), Curated: s.Value.Curated})
	}
	items.Hooks = p.Hooks.Unified.All()
	for _, server := range slices.Sorted(maps.Keys(p.MCP)) {
		items.MCP = append(items.MCP, p.MCP[server])
	}
	return items
}

// block is one engine's block, nil when the item declares none for it.
func block(exports map[string][]byte, name engine.Name) []byte {
	if raw, ok := exports[string(name)]; ok {
		return slices.Clone(raw)
	}
	return nil
}

// IndexOf enumerates the catalog: every fragment, command and skill it
// holds, by bundle then by name, with its kind, description and premise —
// not bytes, and not only the selection.
func IndexOf(cat bundles.Catalog) (Index, error) {
	var idx Index
	seen := map[string]bool{}
	add := func(read bundles.BundleRead, kind trust.ItemKind, name, description, premise string) error {
		ref, err := bundles.ItemRefFor(read.SourceRef(), kind, name)
		if err != nil {
			return fmt.Errorf("composite: %s %q in bundle %q: %w", kind, name, read.DisplayName(), err)
		}
		if seen[ref] {
			return nil
		}
		seen[ref] = true
		idx.Entries = append(idx.Entries, IndexEntry{Ref: ref, Kind: kind, Description: description, Premise: premise})
		return nil
	}
	for _, read := range cat.Reads() {
		b := read.Bundle
		for _, name := range slices.Sorted(maps.Keys(b.Fragments)) {
			if err := add(read, trust.KindFragment, name, "", b.Fragments[name].Premise); err != nil {
				return Index{}, err
			}
		}
		for _, name := range slices.Sorted(maps.Keys(b.Commands)) {
			if err := add(read, trust.KindPrompt, name, b.Commands[name].Description, ""); err != nil {
				return Index{}, err
			}
		}
		for _, name := range slices.Sorted(maps.Keys(b.Skills)) {
			if err := add(read, trust.KindSkill, name, "", ""); err != nil {
				return Index{}, err
			}
		}
	}
	return idx, nil
}
