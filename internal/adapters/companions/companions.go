// Package companions is ctxloom's side of the companion contract: resolve
// each REGISTERED companion name on PATH, exec its loadout, and hand the
// result to a generation as a bundle reader. The companion side — the `loadout` subcommand a companion binary
// wires in — is the loadout subpackage, so a lean binary links nothing of
// the bundle model.
package companions

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// companionProbeTimeout bounds the `<bin> loadout --format json` exec at
// boot. A wedged companion must degrade to a warning, never a stalled
// startup. companionProbeWaitDelay bounds how long Output keeps waiting for
// the stdout pipe to close after the direct child is dead — without it, a
// companion that spawned a grandchild inheriting stdout would stall startup
// forever despite the context kill. Vars (not consts) so tests can shrink them.
// The VERSION probe's equivalents live with that probe, in
// cliversion.ProbeTimeout / cliversion.ProbeWaitDelay.
var (
	companionProbeTimeout   = 3 * time.Second
	companionProbeWaitDelay = time.Second
)

// SetCompanionVersionOutputForTesting overrides the version-probe exec seam
// and returns a restore function. Companion of SetLookPathForTesting.
//
// The seam lives with the probe, in cliversion — the owner of the
// cross-binary `version --format json` contract, and the ONE implementation
// the agent image's version key (internal/adapters/isolation) reads through as
// well. Two probes could disagree about what a companion's version IS, and
// the disagreement would surface as an image that never rebuilds.
func SetCompanionVersionOutputForTesting(fn func(string) ([]byte, error)) func() {
	return cliversion.SetOutputForTesting(fn)
}

// CompanionStatus is one boot-time probe of a registered companion binary — a
// standalone tool (taskloom, ltk) whose loadout wires it into the agent
// session.
type CompanionStatus struct {
	Name    string // the registered name
	Bin     string // the binary the name resolves (BinaryName)
	Path    string // resolved PATH location; empty when not installed
	Version string // self-reported via `<bin> version --format json`
	Err     error  // version-probe failure for a present binary
}

// ProbeCompanions resolves each REGISTERED companion on PATH and asks it for
// its version. A registered name that resolves to nothing yields Path == ""
// (the loadout probe reports it); a present binary whose probe fails carries
// the error. Reporting only — never fatal.
//
// Probes run concurrently: each is bounded by companionProbeTimeout, so a
// sequential loop would add that bound per wedged companion to startup.
// Output order follows names, since each goroutine writes its own slot.
func (p Prober) ProbeCompanions(names []string) []CompanionStatus {
	// Enforced at the exec boundary, not only at each caller: a report path
	// that forgets the switch must still never exec a companion binary.
	if p.Disabled {
		return nil
	}
	out := make([]CompanionStatus, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		st := CompanionStatus{Name: name, Bin: BinaryName(name)}
		r, err := Resolve(name)
		if err != nil {
			out[i] = st
			continue
		}
		st.Path = r.Path
		wg.Add(1)
		go func(i int, st CompanionStatus) {
			defer wg.Done()
			st.Version, st.Err = cliversion.Probe(st.Path)
			out[i] = st
		}(i, st)
	}
	wg.Wait()
	return out
}

// ===== Companion REGISTRATION =====
//
// A companion loadout is a bundle a binary on PATH advertises about itself
// (`<bin> loadout --format yaml`), distinct from the built-in bundles (which
// ship INSIDE the ctxloom binary). Reading one means RUNNING the binary, so
// the control point is which binaries ctxloom runs at all: exactly the names
// a human registered (`ctxloom companion add <name>`, the config's
// `companions` key), each resolved on PATH when it is used. Nothing on PATH
// is ever discovered by scanning — a transitive npm dependency that ships
// ctxloom-companion-<anything> into node_modules/.bin earns nothing by its
// name alone. The registration is a NAME, never a path, so it holds wherever
// the binary is installed; the accepted residual is that a binary of a
// registered name placed earlier on PATH shadows the real one.

// firstPartyCompanions are the shipped companions whose binaries carry their
// own names (they predate the ctxloom-companion-* convention): registering
// one resolves that bare name.
var firstPartyCompanions = []string{"ltk", "taskloom", "reprise"}

// SelfCompanion is the companion identity ctxloom probes ITSELF under —
// ctxloom:companion@ctxloom. It is never registered: ctxloom's own loadout
// comes from the RUNNING binary (Prober.Self — never a PATH lookup, which
// could answer with a stale install, and never a raw os.Executable, which
// goes stale after an in-place upgrade).
const SelfCompanion = agent.CtxloomBinary

// companionPathPrefix is the binary-naming convention for every companion
// that is not first-party: name <n> resolves ctxloom-companion-<n>.
const companionPathPrefix = "ctxloom-companion-"

// ErrInvalidCompanionName refuses a name that could address anything other
// than one binary on PATH: empty, a path, or a flag.
var ErrInvalidCompanionName = errors.New("invalid companion name")

// ErrCompanionNotOnPath is a registered (or to-be-registered) name whose
// binary is not on PATH.
var ErrCompanionNotOnPath = errors.New("companion not found on PATH")

// ErrNotACompanion is a binary that does not answer the companion loadout
// probe with a loadout.
var ErrNotACompanion = errors.New("binary does not answer the companion loadout probe")

// validName is the shape a companion name takes: one path segment of plain
// characters, not starting like a flag. The config schema's companions items
// carry the same pattern.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateName refuses a name that is not a plain companion name.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%w: %q (a companion is registered by name, e.g. `acme` for ctxloom-companion-acme)", ErrInvalidCompanionName, name)
	}
	return nil
}

// BinaryName is the binary a companion name resolves on PATH: a first-party
// name is its own binary, any other is ctxloom-companion-<name>.
func BinaryName(name string) string {
	if slices.Contains(firstPartyCompanions, name) {
		return name
	}
	return companionPathPrefix + name
}

// Resolved is one companion name resolved on PATH now.
type Resolved struct {
	Name string `json:"name" yaml:"name"`
	Bin  string `json:"bin" yaml:"bin"`
	Path string `json:"path" yaml:"path"`
}

// Resolve finds name's binary on PATH. It runs nothing.
func Resolve(name string) (Resolved, error) {
	if err := ValidateName(name); err != nil {
		return Resolved{}, err
	}
	bin := BinaryName(name)
	p, err := lookPath(bin)
	if err != nil {
		return Resolved{Name: name, Bin: bin}, fmt.Errorf("%w: %s (for companion %q): %w", ErrCompanionNotOnPath, bin, name, err)
	}
	return Resolved{Name: name, Bin: bin, Path: p}, nil
}

// Verify resolves name and runs the same loadout probe a session runs,
// requiring it to answer with a loadout: what `companion add` checks before it
// records the name.
func Verify(name string) (Resolved, error) {
	r, err := Resolve(name)
	if err != nil {
		return r, err
	}
	raw, err := companionLoadoutOutput(r.Path)
	if err != nil {
		return r, fmt.Errorf("%w: `%s %s`: %w", ErrNotACompanion, r.Path, strings.Join(loadoutArgs, " "), classifyLoadoutProbe(err))
	}
	if len(raw) == 0 {
		return r, fmt.Errorf("%w: `%s %s` printed no loadout", ErrNotACompanion, r.Path, strings.Join(loadoutArgs, " "))
	}
	return r, nil
}

// warnNotOnPath names a registered companion that resolves to nothing, and
// both ways out.
const warnNotOnPath = "companion %q is registered but %s is not on PATH, so it contributes nothing — install it " +
	"(then `ctxloom companion add %s` checks it), or unregister it: ctxloom companion remove %s"

// lookPath is the PATH-resolution seam: the one place this package asks the
// host which binaries exist. Tests fake it so companion resolution is a
// property of the test, not of the developer's machine.
var lookPath = exec.LookPath

// SetLookPathForTesting overrides the PATH-resolution seam and returns a
// restore function.
func SetLookPathForTesting(fn func(string) (string, error)) func() {
	prev := lookPath
	lookPath = fn
	return func() { lookPath = prev }
}

// companionLoadoutOutput runs a companion's loadout probe; seam for tests,
// mirrors companionVersionOutput exactly (same timeout/WaitDelay discipline
// — a wedged companion degrades to a warning, never a stalled startup).
// A probe the timeout killed returns a SIGNALLED exit error, which
// classifyLoadoutProbe reads as a failure, never as an answer.
var companionLoadoutOutput = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), companionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, loadoutArgs...)
	cmd.WaitDelay = companionProbeWaitDelay
	return cmd.Output()
}

// loadoutArgs is the loadout probe's argv after the binary.
var loadoutArgs = []string{loadout.Subcommand, "--" + loadout.FormatFlag, loadout.FormatYAML}

// SetCompanionLoadoutOutputForTesting overrides the loadout-probe exec seam
// and returns a restore function. Companion of SetCompanionVersionOutputForTesting.
func SetCompanionLoadoutOutputForTesting(fn func(string) ([]byte, error)) func() {
	prev := companionLoadoutOutput
	companionLoadoutOutput = fn
	return func() { companionLoadoutOutput = prev }
}

// Prober is the companion-probing adapter for one invocation. Disabled is
// the --no-companions / CTXLOOM_NO_COMPANIONS switch, a property of the value
// rather than a process global so every exec boundary honours it — no
// companion binary is executed and no companion commands/hooks/MCP/context
// are contributed. Deliberately NO config key: turning companions off is a
// per-invocation or CI decision, not project state someone can leave set and
// later wonder why their ltk commands vanished.
type Prober struct {
	Disabled bool
	// Self arms the self-probe: ctxloom probing ITSELF as a companion (see
	// SelfCompanion). It resolves the RUNNING binary's path at probe time —
	// the composition root injects selfexec.Path, so the answer survives an
	// in-place upgrade and is never a PATH lookup. Only a process composed
	// WITH an embedded loadout sets it (cmd/ctxloom's composition root),
	// because a process without one has nothing to emit, and exec'ing a test
	// binary as if it were ctxloom re-runs the test binary. nil is unarmed.
	// Disabled does NOT disarm it: that switch is about the host's installed
	// binaries, and this is the running one.
	Self func() string
}

// ReaderSource is the per-generation companion reader the composition root
// hands the config Sources: every REGISTERED companion's loadout
// (cfg.GetCompanions), seeded under its ctxloom:companion@<bin> ref. A
// generation resolves its catalog once, so each probe runs once per
// generation.
func (p Prober) ReaderSource() func(cfg *config.Config) []bundles.Reader {
	return func(cfg *config.Config) []bundles.Reader {
		if len(cfg.GetAppPaths()) == 0 {
			return nil
		}
		names := cfg.GetCompanions()
		probe := func(ctx context.Context) (bundles.CompanionProbe, error) {
			return p.ProbeCompanionLoadouts(ctx, names)
		}
		return []bundles.Reader{bundles.NewCompanionReader(probe, bundles.WithReaderReporter(cfg.Reporter()))}
	}
}

// ProbeCompanionLoadouts is the companion reader's EXEC seam: it resolves each
// REGISTERED name on PATH and, for each that resolves, execs
// `<bin> loadout --format yaml`, returning the loadout document's raw bytes.
//
// It stops at BYTES. Parsing them belongs to bundles.NewCompanionReader — one
// place, shared with every other source.
//
// A registered name that resolves to nothing contributes nothing, with a
// warning naming it (warnNotOnPath). A companion that answers it offers no
// loadout contributes nothing, quietly. One whose probe fails or times out
// contributes nothing this time, with a warning (see failedLoadout). NEVER
// fatal, NEVER a crash, NEVER a stalled startup.
//
// Probes run concurrently, each bounded by companionProbeTimeout, so the
// worst-case wall-clock stays ~one timeout however many are registered.
func (p Prober) ProbeCompanionLoadouts(ctx context.Context, names []string) (bundles.CompanionProbe, error) {
	if err := ctx.Err(); err != nil {
		return bundles.CompanionProbe{}, err
	}
	// Disabled (--no-companions) means NO registered binary is executed — the
	// switch makes a run independent of what the host has installed. It does
	// not disarm the self-probe below: ctxloom's own loadout is the running
	// binary's, not something installed on the host, and a run without it
	// would lose ctxloom's MCP server and its always-on guidance.
	var resolved []Resolved
	var candidates []bundles.CompanionCandidate
	if !p.Disabled {
		resolved, candidates = resolveRegistered(names)
	}
	// ctxloom ITSELF, first in the fan-out, probed through the same exec as
	// every other companion so its loadout takes exactly the path theirs does.
	if p.Self != nil {
		resolved = append([]Resolved{{Name: SelfCompanion, Bin: SelfCompanion, Path: p.Self()}}, resolved...)
	}
	slots := make([]*bundles.CompanionLoadout, len(resolved))
	failed := make([]*bundles.CompanionCandidate, len(resolved))
	var wg sync.WaitGroup
	for i, r := range resolved {
		wg.Add(1)
		go func(i int, bin, path string) {
			defer wg.Done()
			slots[i], failed[i] = probeLoadout(bin, path)
		}(i, r.Bin, r.Path)
	}
	wg.Wait()
	return collectProbes(slots, failed, candidates), nil
}

// resolveRegistered resolves each registered name on PATH. One that resolves
// to nothing is warned about and kept as an absent catalog candidate, so a
// report can say the registered companion is missing rather than nothing.
func resolveRegistered(names []string) ([]Resolved, []bundles.CompanionCandidate) {
	resolved := make([]Resolved, 0, len(names)+1)
	var absent []bundles.CompanionCandidate
	for _, name := range names {
		r, err := Resolve(name)
		if err != nil {
			clidiag.WarnOnce("ctxloom", warnNotOnPath, name, BinaryName(name), name, name)
			absent = append(absent, bundles.CompanionCandidate{Bin: BinaryName(name), Reason: bundles.CandidateAbsent})
			continue
		}
		resolved = append(resolved, r)
	}
	return resolved, absent
}

// probeLoadout execs one companion's loadout probe: its loadout, or what
// failedLoadout makes of a probe that produced none.
func probeLoadout(bin, path string) (*bundles.CompanionLoadout, *bundles.CompanionCandidate) {
	raw, err := companionLoadoutOutput(path)
	if err != nil {
		return nil, failedLoadout(bin, path, classifyLoadoutProbe(err))
	}
	if len(raw) == 0 {
		// It exited cleanly and printed nothing: it never answered.
		return nil, failedLoadout(bin, path, fmt.Errorf("%w: it printed no loadout", ErrLoadoutProbeFailed))
	}
	return &bundles.CompanionLoadout{Bin: bin, Path: path, Document: raw, Self: bin == SelfCompanion}, nil
}

// collectProbes gathers the probes' loadouts, in resolution order, and adds
// each failed probe to the candidates.
func collectProbes(slots []*bundles.CompanionLoadout, failed []*bundles.CompanionCandidate, candidates []bundles.CompanionCandidate) bundles.CompanionProbe {
	out := make([]bundles.CompanionLoadout, 0, len(slots))
	for _, lo := range slots {
		if lo != nil {
			out = append(out, *lo)
		}
	}
	for _, c := range failed {
		if c != nil {
			candidates = append(candidates, *c)
		}
	}
	return bundles.CompanionProbe{Loadouts: out, Candidates: candidates}
}
