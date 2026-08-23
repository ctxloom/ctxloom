package ledger

import (
	"fmt"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concurrentWriters is the width of these measurements; 20 matches the figure
// task tall-nanny measured on config Save().
const concurrentWriters = 20

// writeAll runs one Write per surface, all at once, and reports which
// surfaces did not survive. serialize, when non-nil, is the caller-side lock
// this package's own contract says every writer must hold.
func writeAll(t *testing.T, l Ledger, surfaces []Surface, serialize *sync.Mutex) []Surface {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, s := range surfaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if serialize != nil {
				serialize.Lock()
				defer serialize.Unlock()
			}
			_ = l.Write(s, []string{fmt.Sprintf("entry-%02d", i)})
		}()
	}
	close(start)
	wg.Wait()

	var missing []Surface
	for i, s := range surfaces {
		got, err := l.Read(s)
		require.NoError(t, err)
		if len(got) != 1 || got[0] != fmt.Sprintf("entry-%02d", i) {
			missing = append(missing, s)
		}
	}
	return missing
}

func numberedSurfaces(n int) []Surface {
	out := make([]Surface, n)
	for i := range out {
		out[i] = Surface(fmt.Sprintf("surface%02d", i))
	}
	return out
}

// TestLedger_IsNotSelfSerializing_TheCallerMustLock pins the contract this
// package relies on and does not itself provide, because reading the code
// cannot tell you which of the two it is.
//
// Ledger.Write is a read-modify-write: readAll, replace ONE surface's
// entries, rewrite every surface. The rewrite is atomic
// (iox.WriteFileAtomicFs) and there is no lock in this package. MEASURED here
// on the real filesystem: 20 concurrent Writes to 20 DIFFERENT surfaces lose
// 11 to 15 of them. Atomicity prevents a torn marker; it does nothing about
// writer B reading the marker before writer A's rename lands.
//
// That is NOT a defect in this package. The serialization is real and it
// lives at the CALLER: every production writer wraps its whole
// load-modify-save-and-ledger-write cycle in agent.WithFileLock (see
// claude.claudeSettingsWriter, codex's settings writer, opencode's, and
// agent.MCPFileConfig), and tests/arch/lock_discipline_test.go excludes this
// package from its scan for exactly that reason: "internal/shared/ledger and
// internal/shared/filelock are the lock/record PRIMITIVES themselves ...
// their own callers are what must hold the lock."
//
// This test states both halves so the contract is a fact a reader can check
// rather than a claim in a comment: unserialized loses, serialized does not.
// A future change that made Write self-serializing would turn the first half
// red, which is the correct signal to update this test deliberately rather
// than to discover the contract moved.
func TestLedger_IsNotSelfSerializing_TheCallerMustLock(t *testing.T) {
	surfaces := numberedSurfaces(concurrentWriters)

	t.Run("unserialized, as no production caller does: writes are lost", func(t *testing.T) {
		l := Ledger{FS: afero.NewOsFs(), Dir: t.TempDir()}
		missing := writeAll(t, l, surfaces, nil)
		assert.NotEmpty(t, missing,
			"if this ever stops losing writes, Write acquired a serialization of its own and the caller contract below is no longer the thing keeping ctxloom correct")
	})

	t.Run("serialized, as every production caller does: nothing is lost", func(t *testing.T) {
		l := Ledger{FS: afero.NewOsFs(), Dir: t.TempDir()}
		var mu sync.Mutex
		missing := writeAll(t, l, surfaces, &mu)
		assert.Empty(t, missing,
			"a serialized caller must lose nothing; %d of %d were lost, which would mean the loss has a second cause the caller's lock cannot fix",
			len(missing), len(surfaces))
	})
}
