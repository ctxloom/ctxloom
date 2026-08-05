//go:build acceptance

// The trust posture CLI (trust_cli.feature): what `ctxloom trust accept` and
// `ctxloom trust reject` actually DO, as opposed to what they print.
//
// This file exists because the scenario titled "A per-item acceptance and a
// rejection ARE RECORDED" recorded nothing that any assertion looked at. Every
// step was an exit code plus a substring of the output, and "Approved
// demo#fragments/guide" is the argument echoed back — so neutering
// countersign.Store's two write paths, until the store recorded NOTHING AT
// ALL, left all four trust_cli scenarios green (audit irate-catfish, F2). That
// is precisely the product failure j18_signing.feature's @wip scenario
// describes in prose: exit 0, a success message naming the key, and no effect,
// in the flagship trust command.
//
// So the assertions here read the two places a decision can actually be
// observed, and neither is the command's own report of itself:
//
//  1. THE RECORD. countersign.Store's own Has* predicates, asked about the
//     exact (ref, attestation form, payload) triple the CLI should have
//     written — the same lookup EffectiveTrust performs when it decides
//     whether to serve the item. A store that wrote nothing answers false.
//
//  2. THE EFFECT. `ctxloom fragment list --format json`'s "state" — the
//     review-state label a human reads, stamped by the same
//     operations.TrustStamper/EffectiveTrust path materialize uses. The
//     rejected fragment must flip to "rejected" while its untouched sibling
//     stays "accepted": the flip AND the isolation, because a gate that
//     withheld everything would satisfy the first half alone.
//
// LOCAL CONTENT, AND WHY THE ACCEPTANCE IS ASSERTED ON THE RECORD ONLY. This
// fixture's bundle is project-authored, and local content is auto-allowed at
// step 3 of the trust cascade, ahead of any review — so `demo#fragments/guide`
// reads "accepted" BEFORE anybody accepts anything, and a state assertion on
// the accept half would be tautological a second time. The acceptance's one
// observable consequence here is the countersignature, which is exactly what
// (1) reads. The rejection, which beats the local allowance, is asserted both
// ways.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// tcLocalRef composes the countersign ref key for a fragment of a
// PROJECT-AUTHORED bundle, exactly as operations.countersignRef does:
// Ref.CanonicalURL() + "|" + Ref.Key(), both from trust.Ref's own
// canonicalization rather than from a hand-spelled string. Local items key
// under the fixed ctxloom:local source token (Ref.CanonicalURL).
func tcLocalRef(bundle, fragment string) string {
	ref := trust.Ref{Bundle: bundle, Kind: trust.KindFragment, Name: fragment, IsLocal: true}
	return ref.CanonicalURL() + "|" + ref.Key()
}

// tcFragmentPayload returns the countersign payload for a fragment of the
// project's authored bundle: the item's CURRENT bytes run through the
// production preimage builder (bundles.BundleFragment.ContentPayload), so the
// lookup below is over the same bytes the CLI signed rather than over
// something this test re-derived its own way.
func tcFragmentPayload(w *World, bundle, fragment string) ([]byte, error) {
	rel := paths.RepoContentPrefix + "/" + paths.BundlesDir + "/" + bundle + ".yaml"
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("read authored bundle %s: %w", rel, err)
	}
	parsed, err := bundles.ParseBundle([]byte(body))
	if err != nil {
		return nil, fmt.Errorf("parse authored bundle %s: %w", rel, err)
	}
	frag, ok := parsed.Fragments[fragment]
	if !ok {
		return nil, fmt.Errorf("bundle %s ships no fragment %q", bundle, fragment)
	}
	payload, _ := frag.ContentPayload(false)
	if len(payload) == 0 {
		return nil, fmt.Errorf("fragment %s#fragments/%s has an EMPTY content payload — nothing could be countersigned over it", bundle, fragment)
	}
	return payload, nil
}

// tcApprovalsStore opens Alice's user countersignature store — the one
// `trust accept`/`trust reject` write to when no signing key is available
// (spec §9.5's degraded path, which the scenario's "UNSIGNED" assertion pins).
// Reading it through countersign.Store rather than by listing filenames is
// deliberate: the record's identity is a hash of the framed preimage, so only
// the production lookup can say whether the record on disk is the record for
// THESE bytes in THIS role, or merely a file of about the right shape.
func tcApprovalsStore(w *World) *countersign.Store {
	dir := filepath.Join(w.env.HomeDir, paths.AppDirName, paths.ApprovalsDirName)
	return countersign.NewStore(dir, afero.NewOsFs())
}

// tcFragmentStates runs `ctxloom fragment list --format json` and returns each
// listed fragment's review-state label, keyed by "<bundle>#<name>" so a
// fragment of the fixture bundle can never be confused with a same-named one
// from a companion bundle this machine happens to have installed.
func tcFragmentStates(w *World) (map[string]string, error) {
	if err := runOK(w, "fragment", "list", "--format", "json"); err != nil {
		return nil, err
	}
	out := w.env.LastOutput()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("parse `fragment list --format json`: %w\noutput:\n%s", err, out)
	}
	states := make(map[string]string, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		label, _ := row["bundle_label"].(string)
		state, _ := row["state"].(string)
		states[label+"#"+name] = state
	}
	return states, nil
}

func registerTrustCLISteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the approvals store holds an acceptance of "([^"]*)" over the fragment's current bytes$`,
		func(c context.Context, sel string) error {
			w := worldFrom(c)
			bundle, fragment, err := tcSplitSelector(sel)
			if err != nil {
				return err
			}
			payload, err := tcFragmentPayload(w, bundle, fragment)
			if err != nil {
				return err
			}
			refStr := tcLocalRef(bundle, fragment)
			store := tcApprovalsStore(w)
			if !store.HasUnsignedApprove(refStr, signing.AttestFragmentRaw, payload) {
				return fmt.Errorf("the approvals store holds NO acceptance of %s in form %q over its %d current bytes.\n"+
					"`trust accept` reported success — a success message with no record behind it is the whole failure mode "+
					"this assertion exists for.\nctxloom reported:\n%s",
					refStr, signing.AttestFragmentRaw, len(payload), w.env.LastOutput())
			}
			w.docStepMaterialized = fmt.Sprintf("approvals store: approve %s form=%s over %d bytes — recorded",
				refStr, signing.AttestFragmentRaw, len(payload))
			return nil
		})

	ctx.Step(`^the approvals store holds a rejection of "([^"]*)": a sticky ref block and a content block over the same bytes$`,
		func(c context.Context, sel string) error {
			w := worldFrom(c)
			bundle, fragment, err := tcSplitSelector(sel)
			if err != nil {
				return err
			}
			payload, err := tcFragmentPayload(w, bundle, fragment)
			if err != nil {
				return err
			}
			refStr := tcLocalRef(bundle, fragment)
			store := tcApprovalsStore(w)
			// BOTH halves, because they fail independently and mean different
			// things: the ref block is what stops THIS item at THIS name, and
			// the content block is what stops the same bytes republished under
			// another name. A reject that wrote only one of them would still
			// print both lines.
			if !store.HasUnsignedRefReject(refStr) {
				return fmt.Errorf("the approvals store holds NO ref block for %s, though `trust reject` printed "+
					"\"ref block: recorded\"; ctxloom reported:\n%s", refStr, w.env.LastOutput())
			}
			if !store.HasUnsignedContentReject(signing.AttestFragmentRaw, payload) {
				return fmt.Errorf("the approvals store holds NO content block in form %q over the %d bytes of %s, though "+
					"`trust reject` printed \"rejected in form(s) raw\"; ctxloom reported:\n%s",
					signing.AttestFragmentRaw, len(payload), refStr, w.env.LastOutput())
			}
			w.docStepMaterialized = fmt.Sprintf("approvals store: ref-block %s + content-block form=%s over %d bytes — both recorded",
				refStr, signing.AttestFragmentRaw, len(payload))
			return nil
		})

	ctx.Step(`^"([^"]*)" is withheld from the agent, and the bundle's other fragment is not$`,
		func(c context.Context, sel string) error {
			w := worldFrom(c)
			bundle, fragment, err := tcSplitSelector(sel)
			if err != nil {
				return err
			}
			states, err := tcFragmentStates(w)
			if err != nil {
				return err
			}
			got := states[bundle+"#"+fragment]
			if got != "rejected" {
				return fmt.Errorf("%s#fragments/%s reads %q in `fragment list --format json`, want \"rejected\" — "+
					"the rejection must beat the local auto-allowance, not merely be printed; states: %v",
					bundle, fragment, got, states)
			}
			// The ISOLATION half. Without it a gate that withheld everything —
			// or a listing that lost the bundle entirely — would satisfy the
			// assertion above.
			const sibling = "example"
			if other := states[bundle+"#"+sibling]; other != "accepted" {
				return fmt.Errorf("rejecting %s#fragments/%s also took out its untouched sibling %q, which reads %q "+
					"(want \"accepted\"); a per-item rejection must be per-item; states: %v",
					bundle, fragment, sibling, other, states)
			}
			w.docStepMaterialized = fmt.Sprintf("fragment list --format json → %s#%s: %q · %s#%s: %q",
				bundle, fragment, got, bundle, sibling, states[bundle+"#"+sibling])
			return nil
		})
}

// tcSplitSelector splits a "<bundle>#fragments/<name>" selector — the exact
// spelling the feature file hands `trust accept`/`trust reject` — into its two
// components. It fails loud on anything else rather than silently asserting
// about "": an empty bundle or fragment name would make every lookup above
// resolve to nothing and read as a product failure.
func tcSplitSelector(sel string) (bundle, fragment string, err error) {
	const infix = "#fragments/"
	i := strings.Index(sel, infix)
	if i < 1 || i+len(infix) >= len(sel) {
		return "", "", fmt.Errorf("trust_cli: %q is not a \"<bundle>#fragments/<name>\" selector", sel)
	}
	return sel[:i], sel[i+len(infix):], nil
}
