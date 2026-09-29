//go:build arch

package arch

import (
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// ringPrefixes are the directories a ctxloom package may live in: the three
// rings, the toolbox, the family products, the test-only tree and the
// composition roots. The module also carries packages that are not ctxloom's
// to restructure — the standalone `pkg/clifmt` library, the embedded
// `resources` and `container` data, the `scripts/` tools and the `tests/`
// trees — and those roots are tolerated by name.
var ringPrefixes = []string{
	"internal/core",
	"internal/adapters",
	"internal/engines",
	"internal/shared",
	"internal/ltk",
	"internal/taskloom",
	"internal/testsupport",
	"cmd",
	"pkg",
	"resources",
	"container",
	"scripts",
	"tests",
}

// movedAwayPackages are the import paths the ring rename moved a package OUT
// of. Each is asserted absent so that none quietly regrows at its old path —
// most would also trip the ring gate below, but the ones that sat under
// internal/shared/ are inside a ring prefix and only this list catches them.
var movedAwayPackages = []string{
	"internal/agentcoord",
	"internal/agentcoord/coord",
	"internal/agentcoord/coord/coordtest",
	"internal/agentcoord/discover",
	"internal/agentcoord/mcpschema",
	"internal/agentcoord/mcpschema/gen",
	"internal/agentcoord/spool",
	"internal/agents",
	"internal/archlint",
	"internal/buildpins",
	"internal/bundles",
	"internal/claude",
	"internal/claude/engine",
	"internal/cli",
	"internal/cli/tui",
	"internal/compression",
	"internal/config",
	"internal/config/layerscope",
	"internal/confpatch",
	"internal/content",
	"internal/content/archive",
	"internal/content/attest",
	"internal/content/remotetree",
	"internal/contextmetrics",
	"internal/docsgen",
	"internal/enginepins",
	"internal/engineversion",
	"internal/errs",
	"internal/git",
	"internal/gitignore",
	"internal/liveness",
	"internal/lm/conformance",
	"internal/lm/engines",
	"internal/lm/isolation",
	"internal/mcp",
	"internal/memory",
	"internal/mockengine",
	"internal/operations",
	"internal/paths",
	"internal/profiles",
	"internal/projectroot",
	"internal/refuri",
	"internal/remote",
	"internal/schema",
	"internal/schemagen",
	"internal/selfexec",
	"internal/sessions",
	"internal/shared/agent",
	"internal/shared/agent/present",
	"internal/shared/companionloadout",
	"internal/shared/wire",
	"internal/signing",
	"internal/signing/agentkey",
	"internal/signing/allowedsigners",
	"internal/signing/countersign",
	"internal/termui",
	"internal/tmuxhost",
	"internal/transcript",
	"internal/transcript/policy",
	"internal/transcript/vendorreader",
	"internal/transcript/vendorreader/claude",
	"internal/transcript/vendorreader/mock",
	"internal/trust",
	"internal/turnchange",
	"internal/version",
}

// diesInPlace are the packages the design retires rather than moves: the only
// packages permitted outside the rings, each until the slice that deletes it.
var diesInPlace = []string{
	"internal/shared/ledger",
}

// listModulePackages runs `go list ./...` and returns the module-relative
// import paths — every package, test-only ones included, which the source
// scan in arch_test.go deliberately does not see.
func listModulePackages(t *testing.T) map[string]bool {
	t.Helper()
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	pkgs := map[string]bool{}
	for _, ip := range strings.Fields(string(out)) {
		pkgs[strings.TrimPrefix(ip, modulePath+"/")] = true
	}
	if len(pkgs) < 100 {
		t.Fatalf("go list ./... reported %d packages — the listing is broken, not the module", len(pkgs))
	}
	return pkgs
}

// TestArch_MovedPackages_StayGone asserts no moved-away path is a package
// again, and that every dies-in-place exception still names a package — an
// exception for a package already deleted is a stale hole in the ring gate.
func TestArch_MovedPackages_StayGone(t *testing.T) {
	pkgs := listModulePackages(t)
	for _, p := range movedAwayPackages {
		if pkgs[p] {
			t.Errorf("%s is a package again, but the ring rename moved it away — put it in its ring", p)
		}
	}
	for _, p := range diesInPlace {
		if !pkgs[p] {
			t.Errorf("%s is listed as dying in place, but it is no longer a package — delete it from diesInPlace", p)
		}
	}
}

// TestArch_Rings_EveryPackageInsideARing is the confinement gate: `go list
// ./...` shows no package outside the ring directories, the family products,
// the test-only tree and the composition roots, except the diesInPlace
// packages — so a package outside the rings is either unmoved or newly born
// in the wrong place.
func TestArch_Rings_EveryPackageInsideARing(t *testing.T) {
	pkgs := listModulePackages(t)
	var stray []string
	for p := range pkgs {
		if slices.Contains(diesInPlace, p) || archrules.UnderAny(p, ringPrefixes) {
			continue
		}
		stray = append(stray, p)
	}
	sort.Strings(stray)
	for _, p := range stray {
		t.Errorf("package %s is outside every ring directory %v and is not in diesInPlace", p, ringPrefixes)
	}
}
