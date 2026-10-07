package agent

import (
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file holds the GENERIC approaches: implemented once, registered by any
// engine that can use them, imposed on none. They are here because more than
// one engine already uses the mechanism (the native context file) — the test
// for lifting something into shared, not "might a future engine want it".

// NativeContextFile is the native context file every file engine has
// (claude's CLAUDE.md, the mock's context file) at rel beneath the project
// root: where it is presented.
//
// It is a Construct factory rather than a Construct because where the file
// lives is a STATIC fact about the engine, bound at registration.
func NativeContextFile(rel string) Construct {
	return func(SurfaceInputs, safefs.Root) Approach {
		return &nativeContextFile{rel: rel}
	}
}

type nativeContextFile struct {
	rel string
}

// Present declares the native file beneath the advised project root. No flag:
// an engine started in this dir finds it by name.
func (c *nativeContextFile) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(c.rel).Build()
}

var _ Approach = (*nativeContextFile)(nil)
