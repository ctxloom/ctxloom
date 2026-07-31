package cli

import (
	"errors"
	"fmt"
	"testing"
)

// The exit code an error carries must be derivable WITHOUT terminating the
// process. This used to be expressible only as os.Exit(exitErr.Code) inside
// the dispatch function, which is why main's flush never ran on a failing
// run: the process was gone before main got the frame back. Pinning the
// mapping here is what keeps the code a value that can be returned.
func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantOK   bool
	}{
		{"plain error carries no code", errors.New("boom"), 0, false},
		{"ExitError carries its own code", &ExitError{Code: 7}, 7, true},
		{"a zero ExitError is still a carried code", &ExitError{Code: 0}, 0, true},
		{"a wrapped ExitError is still found", fmt.Errorf("dispatch: %w", &ExitError{Code: 130}), 130, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := exitCodeFor(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("exitCodeFor(%v) ok = %v, want %v", tc.err, ok, tc.wantOK)
			}
			if code != tc.wantCode {
				t.Errorf("exitCodeFor(%v) code = %d, want %d", tc.err, code, tc.wantCode)
			}
		})
	}
}
