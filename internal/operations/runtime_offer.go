package operations

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// RuntimeOffer is the set of runtime axes an agent-creating interview may OFFER
// for one engine label, plus the reason the CONTAINER axes are absent when they
// are.
//
// It exists so the interview and the writer cannot disagree. validateContainerAuth
// REFUSES `runtime: container-rootless`/`container-rootful` for an engine with no
// container-auth mapping at WRITE time, so an interview that offered a container
// axis for such an engine would collect a choice the very next `agent create`
// rejects — a confusing interview, not a broken config, but confusing on the one
// question the user has least context to re-derive. The offer therefore consults
// the SAME predicate the refusal does (isolation.HasContainerAuth) instead of
// carrying its own roster of "engines that can be containerized", which is the
// kind of second list that drifts the moment a spec is added.
//
// ContainerWithheld is populated INSTEAD of silently shortening Runtimes. A
// missing option with no explanation reads as ctxloom holding an opinion about
// containers; the actual fact is narrow, engine-specific and fixable, and the
// user is entitled to hear it while they are deciding.
//
// Nothing here is a DEFAULT. Runtimes is an offer — the order is display order,
// not preference, and no element of it is picked for a user who says nothing.
// A container default would turn every unspecified run into an EXPLICIT container
// demand at SelectRuntime, which is fatal (ClassIsolation) on any machine with no
// docker/podman; the interview asks instead, which has no such failure mode.
type RuntimeOffer struct {
	// Label is the engine label the offer was computed for (the value
	// `agent create --engine` accepts), not the backend it resolved to.
	Label string `json:"label"`
	// Backend is the registered backend Label resolved to — the key
	// isolation.HasContainerAuth is actually asked about. Reported because a
	// label and its backend routinely differ (a project's "big" label on
	// backend "codex"), and a withheld container offer is otherwise
	// unattributable.
	Backend string `json:"backend"`
	// Runtimes are the axis values `agent create --runtime` may be offered for
	// this label, in display order. Always at least host.
	//
	// TYPED, not []string: every element is minted below from the runtime
	// vocabulary's own constants, so membership is a property of the type
	// rather than something each reader re-asserts. A []string would oblige
	// every consumer to convert on the way out — an assertion, on the one
	// menu whose whole job is to name exactly what the writer will accept.
	Runtimes []isolation.RuntimeAxis `json:"runtimes"`
	// ContainerWithheld is empty when the container axes ARE offered, and
	// otherwise says why they are not, in the user's terms.
	ContainerWithheld string `json:"container_withheld,omitempty"`
}

// OffersContainer reports whether this offer includes either container axis —
// the question every caller actually has, asked once here rather than
// re-derived from Runtimes at each call site.
func (o RuntimeOffer) OffersContainer() bool {
	for _, r := range o.Runtimes {
		if isolation.IsContainerRuntimeAxis(r) {
			return true
		}
	}
	return false
}

// noConfigRuntimeWithheld is the degraded reason: with no config, the label
// cannot be resolved to a backend, so whether it has container auth is unknown.
// Withholding is the correct DIRECTION for an unknown — offering a container
// axis that the write may then refuse costs the user a wasted decision, while
// withholding one that would have been allowed costs a re-run of the interview
// once the config loads.
const noConfigRuntimeWithheld = "no config could be loaded, so this engine's backend (and whether it can authenticate inside a container) is unknown"

// AgentRuntimeOffer answers "which runtime axes may be offered for this engine
// label". host is ALWAYS offered: it needs nothing installed and no credential
// mapping, and it is what an agent with no runtime key resolves to anyway.
//
// A nil cfg degrades to host alone with noConfigRuntimeWithheld rather than
// failing — this feeds an interview, and an interview must never be blocked by
// a config read (CLAUDE.md fault tolerance).
func AgentRuntimeOffer(cfg *config.Config, label string) RuntimeOffer {
	offer := RuntimeOffer{Label: label, Runtimes: []isolation.RuntimeAxis{isolation.RuntimeHost}}
	if cfg == nil {
		offer.ContainerWithheld = noConfigRuntimeWithheld
		return offer
	}

	backend, _ := ResolveBackend(cfg, label)
	offer.Backend = backend
	if !isolation.HasContainerAuth(backend) {
		offer.ContainerWithheld = fmt.Sprintf(
			"engine %s has no container auth, so a containerized run of it could not authenticate and `agent create --runtime container-rootless` would be refused; engines with container auth: %s",
			describeEngine(label, backend), strings.Join(isolation.ContainerAuthEngines(), ", "))
		return offer
	}

	offer.Runtimes = append(offer.Runtimes,
		isolation.RuntimeContainerRootless,
		isolation.RuntimeContainerRootful)
	return offer
}

// describeEngine names an engine the way a diagnostic should: the label the
// user typed, plus the backend it resolved to whenever those differ. Same
// shape validateContainerAuth's refusal uses, so the interview's explanation
// and the write's refusal read as one voice.
func describeEngine(label, backend string) string {
	if backend != label && backend != "" {
		return fmt.Sprintf("%q (backend %q)", label, backend)
	}
	return fmt.Sprintf("%q", label)
}
