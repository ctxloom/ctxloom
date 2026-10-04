package clidiag

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests install a plain bytes.Buffer as the sink, which is not safe for
// concurrent use. Every Warn helper that targets the sink must therefore be
// serialised inside clidiag: N goroutines released together all write into one
// unsynchronised buffer, which the race detector reports unless clidiag orders
// the writes, and every line must come out whole.
func TestWarn_ConcurrentWarnersIntoAPlainBufferNeverRaceOrInterleave(t *testing.T) {
	ResetWarnOnce()
	t.Cleanup(ResetWarnOnce)
	var buf bytes.Buffer
	restore := SetSink(&buf)

	const goroutines, perGoroutine = 16, 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perGoroutine {
				switch i % 3 {
				case 0:
					Warn("prog", "g%d-n%d", g, i)
				case 1:
					WarnRemedy("prog", "fix-it", "g%d-n%d", g, i)
				default:
					WarnOnce("prog", "g%d-n%d", g, i)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	restore()

	var want []string
	for g := range goroutines {
		for i := range perGoroutine {
			want = append(want, fmt.Sprintf("prog: warning: g%d-n%d", g, i))
		}
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if strings.HasPrefix(l, "prog: warning: ") {
			got = append(got, l)
		}
	}
	assert.ElementsMatch(t, want, got, "every warning line arrives exactly once and whole")
}

// The sink can be swapped while warnings are in flight — a test's restore runs
// while goroutines it started (or a previous test started) are still warning.
// After restore returns, the restored buffer must be readable without racing a
// write still landing in it: restore waits out the in-flight warn. Buffers are
// read only after their own restore, while the warners keep running.
func TestSetSink_RestoreDoesNotRaceAnInFlightWarn(t *testing.T) {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				Warn("prog", "g%d-n%d", g, i)
			}
		}()
	}

	fromWarners := 0
	for range 200 {
		var buf bytes.Buffer
		restore := SetSink(&buf)
		for range 100 {
			Warn("prog", "main")
		}
		restore()
		out := buf.String()
		require.True(t, strings.HasSuffix(out, "\n"), "restored buffer ends on a whole line")
		for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			require.True(t, strings.HasPrefix(l, "prog: warning: "), "torn line %q", l)
		}
		fromWarners += strings.Count(out, "prog: warning: g")
	}
	close(stop)
	wg.Wait()
	require.Positive(t, fromWarners, "background warners must have landed in a swapped-in sink, or the swap race was never exercised")
}
