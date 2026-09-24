package operations

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

var (
	// ErrLaunchFactsNoEngines refuses launch facts with no engine registry:
	// a launch resolves its engine by name and would find none.
	ErrLaunchFactsNoEngines = errors.New("launch facts: no engine registry composed")
	// ErrLaunchFactsNoSessionClaims refuses launch facts with no session
	// claim store: a launch above the inline ceiling would stow its package
	// nowhere.
	ErrLaunchFactsNoSessionClaims = errors.New("launch facts: no session claim store composed")
)

// LaunchFacts is what every launch path needs from the composition root: the
// engine registry, the per-session claim-store constructor, and the posture
// the launch reports and refuses under. It is the ONE source of all three on
// the launch chain; built through NewLaunchFacts, whose Build refuses a
// value missing either composed port.
type LaunchFacts struct {
	Engines       engine.Registry
	SessionClaims launch.SessionClaims
	Mode          strictness.Mode
}

// LaunchFactsBuilder assembles a LaunchFacts; Build validates it.
type LaunchFactsBuilder struct{ f LaunchFacts }

// NewLaunchFacts starts launch facts over reg. It is the ONE place their
// defaults are set, and each is the most restrictive value: Mode is strict
// (never Degraded), so a caller that wants a looser posture says so with
// Mode. Engines and SessionClaims have no default; Build requires them.
func NewLaunchFacts(reg engine.Registry) *LaunchFactsBuilder {
	return &LaunchFactsBuilder{f: LaunchFacts{Engines: reg, Mode: strictness.Mode{Prog: "ctxloom"}}}
}

// Claims sets the per-session claim-store constructor.
func (b *LaunchFactsBuilder) Claims(c launch.SessionClaims) *LaunchFactsBuilder {
	b.f.SessionClaims = c
	return b
}

// Mode sets the posture the launch runs under, replacing the strict default.
func (b *LaunchFactsBuilder) Mode(m strictness.Mode) *LaunchFactsBuilder {
	b.f.Mode = m
	return b
}

// Build returns the facts, or the sentinel naming the missing port: an empty
// registry (ErrLaunchFactsNoEngines) or no claim store
// (ErrLaunchFactsNoSessionClaims).
func (b *LaunchFactsBuilder) Build() (LaunchFacts, error) {
	if len(b.f.Engines.Names(nil)) == 0 {
		return LaunchFacts{}, ErrLaunchFactsNoEngines
	}
	if b.f.SessionClaims == nil {
		return LaunchFacts{}, ErrLaunchFactsNoSessionClaims
	}
	return b.f, nil
}

// LaunchFacts is the facts the composition root handed this App, under its
// strictness. An App composed without them is a composition error with no
// launch to degrade to, so it panics with Build's sentinel.
func (a *App) LaunchFacts() LaunchFacts {
	f, err := NewLaunchFacts(a.engines).Claims(a.claims).Mode(a.Strictness).Build()
	if err != nil {
		panic(err)
	}
	return f
}
