package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestComposeEngines_ANestedStoreIsAdoptedAlone: the composition root hands
// the sessions store each engine's TranscriptStoreRel, so the reap and the
// coordinator's close (both sessions.KeepHomeHistory) move a container run's
// real opencode history store, nested below an XDG base, into native/ alone:
// the session's other content beside it stays in the home.
func TestComposeEngines_ANestedStoreIsAdoptedAlone(t *testing.T) {
	testsupport.Isolate(t)
	if err := composeEngines(); err != nil {
		t.Fatal(err)
	}
	oc, ok := engines.Registry().Lookup(engine.Name("opencode"))
	if !ok {
		t.Fatal("opencode is not composed")
	}
	sd := t.TempDir()
	home, _ := launch.SessionHome(sd, oc, agents.HomeModeSession)
	native, ok := launch.NativeHome(sd, oc, agents.HomeModeSession)
	if !ok {
		t.Fatal("opencode keeps a history store")
	}
	store := filepath.FromSlash(oc.Home().TranscriptStoreRel)
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(native, store, "opencode.db"), "history\n")
	write(filepath.Join(home, store, "opencode.db"), "history\ngrown\n")
	sibling := filepath.Join(filepath.Dir(filepath.Join(home, store)), "sibling", "x")
	write(sibling, "session data\n")

	if err := sessions.KeepHomeHistory(afero.NewOsFs(), sd); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(filepath.Join(native, store, "opencode.db")); err != nil || string(b) != "history\ngrown\n" {
		t.Fatalf("the grown history is not in native/: %q, %v", b, err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("the store's sibling left the home: %v", err)
	}
}
