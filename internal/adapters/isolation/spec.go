package isolation

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// ErrSpecIncomplete is a Spec that cannot build an environment: a required
// fact is missing or malformed. It is wrapped with the field that failed.
var ErrSpecIncomplete = errors.New("isolation: environment spec incomplete")

// Spec is the host configuration an environment is built from: host paths
// and the engine's declared needs only — nothing presented. Built and
// validated by SpecBuilder; the zero Spec is not a usable one.
type Spec struct {
	axes       Axes
	eng        engine.Engine
	project    string
	harp       string
	sessionDir string
	state      SessionState
	img        ImageConfig
	home       agents.HomeMode
}

// SpecBuilder assembles a Spec. The first error wins and is reported at
// Build, so a chain of calls needs no per-call check.
type SpecBuilder struct {
	s   Spec
	err error
}

// NewSpec starts a Spec for the settled axes and the resolved engine the
// run launches.
func NewSpec(axes launch.Axes, eng engine.Engine) *SpecBuilder {
	b := &SpecBuilder{s: Spec{axes: axes, eng: eng, home: agents.HomeModeSession}}
	if eng == nil {
		b.fail("engine", "is required")
	}
	return b
}

// Project names the live project root. Required, absolute.
func (b *SpecBuilder) Project(root string) *SpecBuilder {
	if root == "" || !filepath.IsAbs(root) {
		b.fail("project", fmt.Sprintf("must be an absolute path, got %q", root))
	}
	b.s.project = root
	return b
}

// Session names the run's session: its harp, its directory (where its
// session home is placed, launch.SessionHome) and the identity its state
// mounts are keyed from. Required.
func (b *SpecBuilder) Session(harp, dir string, state SessionState) *SpecBuilder {
	switch {
	case harp == "":
		b.fail("session", "needs a harp")
	case dir == "" || !filepath.IsAbs(dir):
		b.fail("session", fmt.Sprintf("dir must be an absolute path, got %q", dir))
	}
	b.s.harp, b.s.sessionDir, b.s.state = harp, dir, state
	return b
}

// Image is the user's image configuration (overrides, base Containerfile,
// devcontainer detection). The zero value builds the engine's default.
func (b *SpecBuilder) Image(img ImageConfig) *SpecBuilder {
	b.s.img = img
	return b
}

// Home is the binding's engine-home mode. The zero value is the parser's
// default, the session home.
func (b *SpecBuilder) Home(m agents.HomeMode) *SpecBuilder {
	if m == "" {
		m = agents.HomeModeSession
	}
	b.s.home = m
	return b
}

// Build returns the Spec, or the first error wrapped in ErrSpecIncomplete.
func (b *SpecBuilder) Build() (Spec, error) {
	if b.err == nil && b.s.project == "" {
		b.fail("project", "is required")
	}
	if b.err == nil && b.s.harp == "" {
		b.fail("session", "is required")
	}
	if b.err != nil {
		return Spec{}, b.err
	}
	return b.s, nil
}

func (b *SpecBuilder) fail(field, why string) {
	if b.err == nil {
		b.err = fmt.Errorf("%w: %s %s", ErrSpecIncomplete, field, why)
	}
}

// backend is the engine's registered name: what keys its container spec and
// names its runner.
func (s Spec) backend() string { return string(s.eng.Root().Name) }
