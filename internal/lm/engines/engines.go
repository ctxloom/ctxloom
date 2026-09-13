// Package engines is the COMPOSITION ROOT for the engines ctxloom ships: the
// production list that names engine descriptor packages. Adding an engine is
// creating its package, authoring its descriptor there, and adding it to this
// list — no shared table anywhere learns its name.
//
// Registration is explicit and error-returning, not init-time: a bad
// declaration is found here, named, and refused by the process that called
// Register, rather than panicking in whichever engine package inited first.
package engines

import (
	"sync"

	claudeengine "github.com/ctxloom/ctxloom/internal/claude/engine"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

var (
	once sync.Once
	err  error
)

// Register composes every shipped engine into the backend registry. It is
// idempotent per process: the first call does the work and later calls
// return its result, so a test binary and a CLI that both compose see one
// registration.
func Register() error {
	once.Do(func() {
		err = backends.Register(append(backends.MockDescriptors(), claudeengine.Descriptor())...)
	})
	return err
}

// MustRegister is Register for a TestMain, where a composition failure has
// no caller to return to and a panic is the correct loud failure.
func MustRegister() {
	if err := Register(); err != nil {
		panic("engines: " + err.Error())
	}
}
