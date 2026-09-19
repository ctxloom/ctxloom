// Package composite is the gate holder's home: the Trust a config generation
// decides with. It must never know which engine, where files land, or the
// session. Today it holds the gate value the exposure and executable
// resolvers already consult; selection, assembly and the wire form of the
// package arrive with the composite slice.
package composite

import "github.com/ctxloom/ctxloom/internal/core/bundles"

// Trust is the gate holder. It has NO permissive zero value: a zero Trust
// holds no gate, and the nil authorizer it yields is the spelling
// bundles.Decide withholds on loudly ("this surface forgot its gate"), never
// an admit. The two ways to obtain one say what they hold — FromAuthorizer
// wraps a deciding gate; Ungated is the by-name opt-in of the listing and
// review surfaces that must show pending content to a human.
type Trust struct {
	gate bundles.Authorizer
}

// FromAuthorizer holds a deciding gate.
func FromAuthorizer(gate bundles.Authorizer) Trust { return Trust{gate: gate} }

// Ungated is the ONLY way to obtain a Trust that admits everything, opted
// into BY NAME at the surfaces that resolve pending content so a human can
// review, accept or stamp it.
func Ungated() Trust { return Trust{gate: bundles.AdmitAll()} }

// Authorizer is the gate the resolvers consult. Nil for a zero Trust, which
// bundles.Decide withholds on and names.
func (t Trust) Authorizer() bundles.Authorizer { return t.gate }

// Gates reports whether this Trust will actually decide anything: false only
// for Ungated. A zero Trust gates — its nil authorizer stays on the deciding
// path and is withheld there.
func (t Trust) Gates() bool { return bundles.Gates(t.gate) }
