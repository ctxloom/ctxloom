package acptest

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidator_ConcurrentValidateDef pins the promise in NewValidator's doc
// comment that the Validator is "Safe for concurrent use after
// construction": a conformance harness that validates a whole captured
// session will fan its frames out across goroutines on the strength of that
// promise. Before the fix the promise was false — def() read v.cache, then
// wrote it, with no synchronisation at all, and drove jsonschema.Compiler
// (which documents no concurrency guarantee of its own) from every caller at
// once. Under -race that is a reported DATA RACE; unraced it is a
// "concurrent map writes" fatal that takes the whole process down.
//
// The assertion is deliberately on the RESULTS, not just on surviving: a
// cache guarded by a lock that returned a half-built entry would still pass a
// "did not crash" test. Every goroutine must see the same verdict the
// single-threaded path gives.
func TestValidator_ConcurrentValidateDef(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	// Several distinct $defs so the cache takes real WRITES concurrently
	// (one shared name would mostly exercise the read path after the first
	// compile won), each repeated so reads race writes too.
	defs := []string{
		"NewSessionResponse",
		"WriteTextFileResponse",
		"SessionNotification",
		"RequestPermissionRequest",
	}
	const perDef = 16

	var wg sync.WaitGroup
	errs := make([]error, len(defs)*perDef)
	start := make(chan struct{})
	for i, name := range defs {
		for j := 0; j < perDef; j++ {
			wg.Add(1)
			go func(slot int, defName string) {
				defer wg.Done()
				<-start
				// A payload that is valid for none of these object-typed
				// $defs, so the outcome is a stable, comparable error rather
				// than depending on each schema's required fields.
				errs[slot] = v.ValidateDef(defName, json.RawMessage(`null`))
			}(i*perDef+j, name)
		}
	}
	close(start) // release them together, so the compiles overlap
	wg.Wait()

	for i, got := range errs {
		require.Error(t, got, "slot %d: a null payload must be refused by every $defs entry here", i)
		assert.NotContains(t, got.Error(), "compile $defs/",
			"slot %d: compilation must not fail under concurrency", i)
	}
}
