package acp

import (
	"context"
	"sync"
)

// fakeTmuxRunner is a scriptable tmuxhost.Runner, so the terminal/* wire tests
// in this package can drive create -> output -> wait -> kill -> release without
// a real tmux binary.
//
// It is a LOCAL COPY of the fake tmuxhost's own tests use, deliberately rather
// than a helper exported from tmuxhost. Exporting a testing seam would put a
// test-only type on the surviving package's public surface purely to serve this
// one — and this package is scheduled for deletion, at which point the export
// would have no caller left and no obvious reason to go. Thirty duplicated
// lines in the dying half is the cheaper of the two.
type fakeTmuxRunner struct {
	mu    sync.Mutex
	calls [][]string
	// failAll, if set, makes every tmux call fail with it — the "tmux is not
	// installed" case.
	failAll error
}

func newFakeTmuxRunner() *fakeTmuxRunner { return &fakeTmuxRunner{} }

func (f *fakeTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	return "", f.failAll
}

// calledWith reports whether any tmux invocation used sub as its subcommand.
func (f *fakeTmuxRunner) calledWith(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}
