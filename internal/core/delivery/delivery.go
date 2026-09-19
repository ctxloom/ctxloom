// Package delivery plans which package items go STATIC and which DYNAMIC for
// an engine: the Plan is ROUTES over the cell's roots, computed once per
// launch in launch.Resolve and carried on the Launch. The two delivery ports
// (Static, Dynamic) and the ownership record arrive with the writers. It
// must never know engine argv, transport, or config. Imports: engine,
// present — nothing imports delivery but launch.
package delivery

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// Preference is the binding's delivery preference: per kind, the ROOT it
// selects among those the engine's approach offers (the shared project root
// is selected here, never fallen back to), and the kinds whose absence it
// accepts. Validated when the binding is written so a run never sees a root
// the approach does not offer.
type Preference struct {
	Root       map[present.Kind]present.RootKind
	AcceptLoss map[present.Kind]bool
}

// Plan is the loadout as ROUTES: per static Kind the engine's approach and
// the ONE root it will write under; the kinds the engine delineated as
// dynamic, served on the session endpoint; the accepted losses. It carries
// no bytes — the bytes ride once, in the package.
type Plan struct {
	Static  []StaticItem
	Dynamic []string // refs of the preface items served on the endpoint (Base.Delegate's decision)
	Losses  []Loss
}

// StaticItem is one route: the kind, the approach that carries it, the root
// it lands under and the approach's declared traits.
type StaticItem struct {
	Kind     present.Kind
	Approach string
	Root     present.RootKind
	Traits   present.Traits
}

// Loss is a kind the engine does not carry and the binding accepted losing.
type Loss struct{ Kind present.Kind }

// ErrUnrootable is Route's refusal of a root the approach does not offer or
// the cell does not have; Unrootable carries the remedy.
var ErrUnrootable = errors.New("delivery: the approach cannot root under this target")

// ErrUncarried: the package needs a Kind whose typed field on the engine's
// Definition is nil and the binding did not accept the loss. The engine is
// named so the remedy is unambiguous; there is no reason string because the
// absence of an approach IS the reason.
type ErrUncarried struct {
	Engine engine.Name
	Kind   present.Kind
}

func (e ErrUncarried) Error() string {
	return fmt.Sprintf("delivery: engine %q has no approach for kind %v and the binding did not accept the loss", e.Engine, e.Kind)
}

// Unrootable is ErrUnrootable with the remedy: which approach, which root
// was needed (selected by the binding, or the approach's default) and is
// either not offered by the approach or absent from the cell, and what the
// human changes. Delivery REFUSES; it never substitutes another root.
type Unrootable struct {
	Kind     present.Kind
	Approach string
	Needs    present.RootKind
	Remedy   string
}

func (u Unrootable) Error() string {
	return fmt.Sprintf("%v: %s for kind %v needs root %v; %s", ErrUnrootable, u.Approach, u.Kind, u.Needs, u.Remedy)
}

// Unwrap makes errors.Is(err, ErrUnrootable) true.
func (u Unrootable) Unwrap() error { return ErrUnrootable }

// Route decides the plan. ONE place decides the compounding of static and
// dynamic delivery: Route calls root.Delegate(items) and APPLIES its
// Delegation — it never re-decides which item goes to which half. Then, per
// static Kind the delegation lists, reading root.Surfaces()[kind] and
// root.Name and nothing else about the engine:
//  1. the engine carries the Kind (its typed field is non-nil):
//     a. the binding selected a root → that root, if the approach offers it
//     AND the cell has it (else Unrootable, with the remedy);
//     b. else the first root the approach offers that the cell has (none:
//     Unrootable);
//  2. else the Kind is uncarried (nil) → ErrUncarried{Engine, Kind}, unless
//     AcceptLoss names the Kind: then it is routed to NOTHING and listed in
//     Plan.Losses.
//
// There is no arm 3. A Plan always has a non-nil Static so "a plan exists
// even when empty" is a value, not a nil.
func Route(items engine.Items, root engine.Base, pref Preference, roots present.Paths) (Plan, error) {
	d := root.Delegate(items)
	plan := Plan{Static: []StaticItem{}, Dynamic: d.Dynamic}
	surfaces := root.Surfaces()
	for _, kind := range d.Static {
		a, carried := surfaces[kind]
		if !carried {
			if pref.AcceptLoss[kind] {
				plan.Losses = append(plan.Losses, Loss{Kind: kind})
				continue
			}
			return Plan{}, ErrUncarried{Engine: root.Name, Kind: kind}
		}
		traits := a.Traits()
		item := StaticItem{Kind: kind, Approach: a.Name(), Traits: traits}
		if selected, ok := pref.Root[kind]; ok {
			if !traits.Offers(selected) {
				return Plan{}, Unrootable{Kind: kind, Approach: a.Name(), Needs: selected,
					Remedy: fmt.Sprintf("the binding selects root %v for kind %v but approach %s offers %v; select one of those on the binding", selected, kind, a.Name(), traits.Roots)}
			}
			if !has(roots, selected) {
				return Plan{}, Unrootable{Kind: kind, Approach: a.Name(), Needs: selected,
					Remedy: fmt.Sprintf("the binding selects root %v for kind %v but this cell has no such root; select a root the cell provides", selected, kind)}
			}
			item.Root = selected
			plan.Static = append(plan.Static, item)
			continue
		}
		for _, r := range traits.Roots {
			if has(roots, r) {
				item.Root = r
				break
			}
		}
		if item.Root == 0 {
			return Plan{}, Unrootable{Kind: kind, Approach: a.Name(), Needs: traits.Roots[0],
				Remedy: fmt.Sprintf("approach %s offers roots %v and this cell has none of them; give the cell a session home or select a root on the binding", a.Name(), traits.Roots)}
		}
		plan.Static = append(plan.Static, item)
	}
	return plan, nil
}

// has reports whether the cell resolved the root a RootKind names: the
// session home is the run's scratch root; the project root and the work
// dir are the cell's project root (inside the cell the workspace IS the
// project root).
func has(roots present.Paths, r present.RootKind) bool {
	switch r {
	case present.RootSessionHome:
		return roots.Scratch.Host != ""
	case present.RootProjectRoot, present.RootWorkDir:
		return roots.ProjectRoot.Host != ""
	}
	return false
}
