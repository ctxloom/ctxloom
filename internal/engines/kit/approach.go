package kit

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// Approach is the name and traits every typed approach carries, and the one
// rule for rooting what it delivers. An engine embeds it in each of its
// typed approaches and supplies the content (the file, the claims) itself.
type Approach struct {
	// Engine names the engine in a refusal's wording only; it is never
	// compared.
	Engine       engine.Name
	ApproachName string
	T            present.Traits
	// Private requires the session home to be rooted
	// (agent.SessionHomeRooted) before anything is delivered under it: a run
	// with no engine home advised is refused rather than served from the
	// user's own home. Off for an engine whose session-home form tolerates an
	// unrooted start (a test double).
	Private bool
}

// Name is the approach's name, the one a binding selects.
func (a Approach) Name() string { return a.ApproachName }

// Traits is the approach's declared facts.
func (a Approach) Traits() present.Traits { return a.T }

// ErrRoot is the refusal for a root the approach does not offer: the plan
// selected one outside Traits().Roots.
func (a Approach) ErrRoot(root present.RootKind) error {
	return fmt.Errorf("%s/%s: root %v is not one this approach offers", a.Engine, a.ApproachName, root)
}

// Rooted composes the root the plan selected: homeRel beneath the session
// home (behind the Private guard), projectRel beneath the project root. A
// root the traits do not offer is refused (ErrRoot).
func (a Approach) Rooted(start present.Start, root present.RootKind, homeRel, projectRel string) (present.Rooted, error) {
	if !a.T.Offers(root) {
		return present.Rooted{}, a.ErrRoot(root)
	}
	switch root {
	case present.RootSessionHome:
		if a.Private {
			if err := agent.SessionHomeRooted(start); err != nil {
				return present.Rooted{}, err
			}
		}
		return start.UnderSessionHome(homeRel), nil
	case present.RootProjectRoot:
		return start.UnderProjectRoot(projectRel), nil
	}
	return present.Rooted{}, a.ErrRoot(root)
}
