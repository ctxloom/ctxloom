package dockergate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// RemoveBeforeCreateWrapper writes a wrapper around the runtime binary bin that
// FORCES one interleaving of a container teardown against an in-flight launch
// of the container called name, and returns the wrapper's path — use it as the
// runtime's Binary for both the launch and the teardown:
//
//   - `run` is held until teardown has begun (the first `rm` or `container …`
//     inspect), so the daemon has not been asked to create anything when the
//     teardown's first remove reaches it;
//   - that first `rm` really runs — and so deterministically answers "No such
//     container" — then does not return until the daemon HAS registered the
//     container, so whatever the teardown does next lands after the create.
//
// That is the window every teardown-by-name is exposed to: the launch CLI is
// spawned, and its handle handed out, before the daemon has created anything.
func RemoveBeforeCreateWrapper(t testing.TB, bin, name string) string {
	t.Helper()
	dir := t.TempDir()
	began := filepath.Join(dir, "teardown-began")
	firstRm := filepath.Join(dir, "first-rm")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
run)
	i=0
	while [ ! -e %[1]q ]; do
		i=$((i+1)); [ $i -gt 2000 ] && { echo "wrapper: teardown never began" >&2; exit 97; }
		sleep 0.01
	done
	exec %[4]q "$@" ;;
rm)
	touch %[1]q
	if [ -e %[2]q ]; then exec %[4]q "$@"; fi
	touch %[2]q
	err=$(%[4]q "$@" 2>&1 >/dev/null); rc=$?
	i=0
	until %[4]q container inspect %[3]q >/dev/null 2>&1; do
		i=$((i+1)); [ $i -gt 2000 ] && break
		sleep 0.01
	done
	printf '%%s\n' "$err" >&2
	exit $rc ;;
container)
	touch %[1]q
	exec %[4]q "$@" ;;
esac
exec %[4]q "$@"
`, began, firstRm, name, bin)
	path := filepath.Join(dir, filepath.Base(bin)+"-rmrace")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write the rm-race wrapper: %v", err)
	}
	return path
}
