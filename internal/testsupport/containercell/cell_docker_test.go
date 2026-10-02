//go:build docker_integration

// The THREE-RUNTIME MATRIX for the hermetic container cell: docker rootful,
// docker rootless, podman.
//
// WHY A MATRIX AND NOT ONE RUNTIME. The three do not differ in whether a
// container can write a file; they differ in WHO OWNS the file that comes out of
// the bind mount, and that is not something a byte comparison can see. A rootful
// daemon writing through a mount leaves root-owned files in the invoking user's
// tree with byte-identical content and identical POSIX modes — the assertion
// that catches it is ownership, and nothing else does. That is why every cell
// below asserts bytes AND mode AND ownership, and why the ownership assertion is
// not optional decoration.
//
// THE MATRIX IS ASYMMETRIC BY ENVIRONMENT, on purpose. No host is both rootful
// and rootless, and most have no podman, so no single runner covers all three:
// each covers what it has, and the union across runners is the coverage. That
// makes "which runner covers what" a claim someone has to make, which is what
// CTXLOOM_REQUIRE_RUNTIMES is for — a lane that says it covers podman and finds
// none goes RED here rather than skipping green.
//
// CTXLOOM_REQUIRE_DOCKER=1 still gates the matrix as a whole: it does not say
// which runtime, but it does say that at least one cell must have run, so a
// runner that lost its socket cannot pass this file having executed nothing.

package containercell_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// TestContainerCell_DeliversAcrossTheProcessBoundary runs ctxloom INSIDE a
// container of each available runtime, delivering the mock backend's surfaces
// into a bind-mounted target, and asserts from the HOST side of that mount.
func TestContainerCell_DeliversAcrossTheProcessBoundary(t *testing.T) {
	ctx := t.Context()

	// A typo in CTXLOOM_REQUIRE_RUNTIMES matches no runtime, so every cell would
	// skip and this file would report green having covered nothing declared.
	// Reject it before probing anything.
	if err := dockergate.ValidateRequiredRuntimes(containercell.Names()); err != nil {
		t.Fatal(err)
	}

	found := containercell.Detect(ctx)
	// Printed on every run, passing or skipping: a run that covered one runtime
	// must be distinguishable from one that covered three.
	t.Log("\n" + containercell.Report(found))

	anyAvailable := false
	for _, r := range found {
		anyAvailable = anyAvailable || r.Available
	}
	dockergate.RequireRuntime(t, anyAvailable, "the container-cell delivery matrix (docker rootful, docker rootless, podman)")

	for _, rt := range found {
		t.Run(rt.Name, func(t *testing.T) {
			dockergate.RequireNamedRuntime(t, rt.Name, rt.Available,
				"the container cell's bytes/mode/ownership delivery check")
			assertCellDelivers(t, ctx, rt)
		})
	}
}

// assertCellDelivers is one matrix cell: build a project on the host, run
// ctxloom in a container of this runtime, and observe the mount from the host.
func assertCellDelivers(t *testing.T, ctx context.Context, rt containercell.Runtime) {
	t.Helper()
	root := t.TempDir()
	project := writeCellFixture(t, root)
	if err := requireCellBundle(project); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")

	res, err := rt.Run(ctx, containercell.Spec{
		Mounts:  []string{root},
		WorkDir: project,
		// `FROM scratch` inherits no environment at all, so HOME — the only
		// variable ctxloom needs here — is passed explicitly. Its absence would
		// send the user-level config resolution somewhere unwritable and the
		// run would fail rather than deliver, which is the honest outcome.
		Env:  map[string]string{"HOME": filepath.Join(root, "home")},
		Args: []string{"profile", "materialize", "default", "--target", target, "--backend", "mock"},
	})
	if err != nil {
		t.Fatalf("the %s cell could not run: %v", rt.Name, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("in-container `ctxloom profile materialize` exited %d under %s\nargv: %s\noutput:\n%s",
			res.ExitCode, rt.Name, strings.Join(res.Argv, " "), res.Output)
	}

	// The exit code is NOT the assertion. A run that exits 0 having written
	// nothing is this codebase's characteristic bug, and the whole reason the
	// cell exists is to make the container half of it observable — so every
	// claim below is on delivered payload.
	contextFile := filepath.Join(target, "MOCK_CONTEXT.md")
	assertDelivered(t, contextFile, 0o600, "the engine's context file", res)
	body := readFile(t, contextFile)
	if !strings.Contains(body, strings.TrimSpace(fragmentBody)) {
		t.Fatalf("the context file delivered by the %s cell does not carry the fragment body verbatim\n--- published ---\n%s\n--- delivered (%d bytes) ---\n%s\n--- container output ---\n%s",
			rt.Name, fragmentBody, len(body), body, res.Output)
	}

	skill := filepath.Join(target, ".mock", "skills", "reviewer", "SKILL.md")
	assertDelivered(t, skill, 0o644, "the skill package's SKILL.md", res)
	if got := readFile(t, skill); !strings.Contains(got, skillBody) {
		t.Fatalf("SKILL.md delivered by the %s cell lost its bytes:\n%s", rt.Name, got)
	}

	// The exec bit is the one POSIX mode that is load-bearing rather than
	// cosmetic, and 0644 on SKILL.md beside it is the claim that a blanket
	// chmod did not smear it across the package.
	script := filepath.Join(target, ".mock", "skills", "reviewer", "scripts", "run.sh")
	assertDelivered(t, script, 0o755, "the skill package's executable script", res)
	if got := readFile(t, script); got != scriptBody {
		t.Fatalf("the script delivered by the %s cell differs from what was published\n--- published ---\n%s\n--- delivered ---\n%s",
			rt.Name, scriptBody, got)
	}
}

// assertDelivered is the three-part claim every artifact carries: it EXISTS on
// the host side of the mount, with the mode the publisher declared, owned by the
// user running the test.
func assertDelivered(t *testing.T, path string, wantMode os.FileMode, what string, res containercell.Result) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s never reached the host side of the mount at %q: %v\nargv: %s\ncontainer output:\n%s",
			what, path, err, strings.Join(res.Argv, " "), res.Output)
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Fatalf("%s at %q has mode %04o, want %04o — the POSIX mode did not survive the process boundary", what, path, got, wantMode)
	}
	if err := containercell.AssertOwnedByInvoker(path, what); err != nil {
		t.Fatal(err)
	}
}
