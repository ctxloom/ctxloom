package operations

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// =============================================================================
// Plugin Listing Tests
// =============================================================================
// Available plugin list is shown to users in help and error messages.
// Moved here from internal/cli (ISO0 extraction) alongside AvailableLLMNames.

func TestAvailableLLMNames_IncludesBuiltIns(t *testing.T) {
	// All built-in plugins must appear in the available list
	cfg := &config.Config{}
	names := AvailableLLMNames(cfg)

	expected := map[string]bool{
		"claude-code": false,
		"codex":       false,
		"kiro":        false,
	}

	for _, name := range names {
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected %s in available plugin names", name)
		}
	}
}

func TestAvailableLLMNames_Sorted(t *testing.T) {
	// Sorted output provides consistent, scannable display to users
	cfg := &config.Config{}
	names := AvailableLLMNames(cfg)

	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("expected sorted names, but %q < %q at index %d", names[i], names[i-1], i)
		}
	}
}

// =============================================================================
// SetDefaultLLM: the write path, migrated onto Manager.Update
// =============================================================================

// TestSetDefaultLLM_SetsAndPersists proves the write survives a reload. The
// seed names an explicit starting primary ("codex") rather than leaving
// llm.defaults.primary absent — an absent primary is filled in-memory by the
// shipped-default overlay (mergeDefaultConfig) at load time, which would make
// "claude-code" look already-current and turn this into an unchanged-status
// test instead of the set-status one it's named for.
func TestSetDefaultLLM_SetsAndPersists(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\nllm:\n  defaults:\n    primary: codex\n")
	mgr := managerFor(appDir)

	res, err := SetDefaultLLM(context.Background(), mgr, SetDefaultLLMRequest{Name: "claude-code"})
	require.NoError(t, err)
	assert.Equal(t, "set", res.Status)
	assert.Equal(t, "claude-code", res.Name)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "claude-code", reloaded.PrimaryLabel())
}

// TestSetDefaultLLM_UnchangedWhenAlreadyDefault proves the "already the
// default" report and skips the write.
func TestSetDefaultLLM_UnchangedWhenAlreadyDefault(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\nllm:\n  defaults:\n    primary: claude-code\n")
	mgr := managerFor(appDir)

	res, err := SetDefaultLLM(context.Background(), mgr, SetDefaultLLMRequest{Name: "claude-code"})
	require.NoError(t, err)
	assert.Equal(t, "unchanged", res.Status)
}

// TestSetDefaultLLM_EmptyName errors rather than clearing the default.
func TestSetDefaultLLM_EmptyName(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	_, err := SetDefaultLLM(context.Background(), managerFor(appDir), SetDefaultLLMRequest{Name: ""})
	assert.Error(t, err)
}

// TestSetDefaultLLM_UnchangedCheckSeesConcurrentWrite proves the specific
// race Manager.Update closes for this call: the "is this already the
// default" check reads the SAME locked, freshly-reloaded Draft the write
// applies to, so writer B's decision can never be a statement about a config
// writer A has already replaced. This is a different concurrency property
// than TestSetAgent_ConcurrentWritesAllSurvive (N independent map keys
// surviving a race) — SetDefaultLLM has only one scalar to write, so the
// meaningful race here is the read-then-branch (unchanged vs set), not
// "how many distinct writes survive".
//
// Writer A is parked mid-transaction (inside fn, lock held) setting the
// default to "beta". Writer B — a full SetDefaultLLM("beta") call — starts
// concurrently and must be UNABLE to complete while A holds the lock; once A
// commits, B's fresh reload must see "beta" already in place and report
// "unchanged", never "set" against the stale pre-lock "alpha".
func TestSetDefaultLLM_UnchangedCheckSeesConcurrentWrite(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\nllm:\n  defaults:\n    primary: alpha\n")
	mgr := managerFor(appDir)

	aEnteredCritical := make(chan struct{})
	releaseA := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		err := mgr.Update(func(d *config.Draft) error {
			close(aEnteredCritical)
			<-releaseA
			d.LM.Defaults.Primary = "beta"
			return nil
		})
		assert.NoError(t, err)
	}()
	<-aEnteredCritical // A now holds the lock, blocked inside fn.

	bDone := make(chan struct{})
	var bResult *SetDefaultLLMResult
	var bErr error
	go func() {
		defer close(bDone)
		bResult, bErr = SetDefaultLLM(context.Background(), mgr, SetDefaultLLMRequest{Name: "beta"})
	}()

	// B's call is now racing against A's lock. Give it every chance to
	// (wrongly) complete while A still holds the lock.
	select {
	case <-bDone:
		t.Fatal("SetDefaultLLM(\"beta\") completed while writer A still held the lock — " +
			"the unchanged-check is not reading from inside the locked transaction")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseA)
	<-aDone
	<-bDone

	require.NoError(t, bErr)
	assert.Equal(t, "unchanged", bResult.Status,
		"B's fresh reload (taken AFTER acquiring the lock) must see A's already-committed \"beta\", not the stale pre-lock \"alpha\"")

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "beta", final.PrimaryLabel())
}
