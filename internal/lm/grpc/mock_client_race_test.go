package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// MockClient is handed to several callers at once (the compactor distills
// chunks concurrently through a single shared instance via MockClientFactory),
// so every call counter must be safe to increment from many goroutines. A
// counter incremented without the mutex both trips the race detector and loses
// updates, which silently turns a call-count assertion in some other test into
// a coin flip.
func TestMockClient_EveryCallCounterIsConcurrencySafe(t *testing.T) {
	const goroutines = 32

	m := &MockClient{}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Info(ctx)
			_, _ = m.Run(ctx, &RunStart{}, nil, nil, nil, nil)
			_, _ = m.RunWithModelInfo(ctx, &RunStart{}, nil, nil, nil, nil)
			_, _ = m.GetSession(ctx, "s")
			_, _, _ = m.WatchSession(ctx, "s")
			_, _ = m.ListSessions(ctx)
			m.Kill()
		}()
	}
	wg.Wait()

	assert.Equal(t, goroutines, m.InfoCalls, "InfoCalls")
	assert.Equal(t, goroutines, m.RunCalls, "RunCalls")
	assert.Equal(t, goroutines, m.RunWithModelInfoCalls, "RunWithModelInfoCalls")
	assert.Equal(t, goroutines, m.GetSessionCalls, "GetSessionCalls")
	assert.Equal(t, goroutines, m.WatchSessionCalls, "WatchSessionCalls")
	assert.Equal(t, goroutines, m.ListSessionsCalls, "ListSessionsCalls")
	assert.Equal(t, goroutines, m.KillCalls, "KillCalls")
}
