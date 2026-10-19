//go:build !linux

package operations

// listProcesses has no /proc to read here, so the gate refuses.
func listProcesses() ([]RunningProcess, error) { return nil, ErrProcessListUnavailable }

// processVersion is never reached: listProcesses lists nothing here.
func processVersion(RunningProcess) ([]byte, error) { return nil, ErrProcessListUnavailable }
