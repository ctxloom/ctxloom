//go:build !linux

package mountns

import (
	"context"
	"fmt"
	"os/exec"
)

// Command always fails away from Linux: user and mount namespaces are a Linux
// facility, so there is no shim to build.
func Command(_ context.Context, _ []Bind, _ []string, _ []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("%w: user and mount namespaces are a Linux facility", ErrUnsupported)
}

// Supported always fails away from Linux — for a reason that is a property of
// the platform rather than of its configuration, so unlike the Linux answer it
// does not depend on running anything.
func Supported(_ context.Context, _ string) error {
	return fmt.Errorf("%w: user and mount namespaces are a Linux facility", ErrUnsupported)
}

// RunChildIfRequested is a no-op away from Linux: nothing here ever re-execs
// itself as a shim, so there is no marker to honour.
func RunChildIfRequested() {}
