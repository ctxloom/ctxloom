package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// EVERY REAP OUTCOME HERE IS ASSERTED ON THE FILESYSTEM: which sidecars exist
// after. The report's counts are checked beside that, never instead of it.

const gateSelf = 4242

// currentGate is a gate whose every source answers "nothing older is
// running": this process, a current ltk, and no container.
func currentGate() LockReapGate {
	return LockReapGate{
		Self: gateSelf,
		Processes: func() ([]RunningProcess, error) {
			return []RunningProcess{
				{PID: gateSelf, Comm: "ctxloom", Exe: "/usr/local/bin/ctxloom"},
				{PID: 10, Comm: "ltk", Exe: "/usr/local/bin/ltk"},
			}, nil
		},
		Version:    func(RunningProcess) ([]byte, error) { return []byte(`{"name":"ltk","version":"v1","lock_protocol":2}`), nil },
		Containers: func(context.Context) ([]string, error) { return nil, nil },
	}
}

// sidecarFixture is a home lock dir holding one sidecar of every kind the
// step tells apart.
type sidecarFixture struct {
	orphanRecorded, inUseRecorded string // record a path: gone / present
	unreapableHashed              string // a FlatName name, no record
	orphanLegacy, inUseLegacy     string // legacy unhashed names: gone / present
	ambiguousLegacy               string // legacy, but "___" in the name
}

func (f sidecarFixture) all() []string {
	return []string{f.orphanRecorded, f.inUseRecorded, f.unreapableHashed, f.orphanLegacy, f.inUseLegacy, f.ambiguousLegacy}
}

// newSidecarFixture isolates HOME and lays out the fixture's sidecars, each
// made the way its kind really is: recorded ones by safefs.LockGuarding on
// paths.HomePathFor, the rest as the empty files older builds left.
func newSidecarFixture(t *testing.T) sidecarFixture {
	t.Helper()
	testsupport.Isolate(t)
	work := t.TempDir()
	present := filepath.Join(work, "present", ".mcp.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(present), 0o755))
	require.NoError(t, os.WriteFile(present, []byte("{}"), 0o644))
	gone := filepath.Join(work, "gone", ".mcp.json")

	recorded := func(target string) string {
		lp, err := paths.HomePathFor(target)
		require.NoError(t, err)
		lk, err := safefs.LockGuarding(safefs.New().Locks, lp, target)
		require.NoError(t, err)
		require.NoError(t, lk.Unlock())
		return lp
	}
	dir, err := paths.HomeLocksDir()
	require.NoError(t, err)
	empty := func(name string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, nil, 0o644))
		return p
	}
	legacy := func(abs string) string { return strings.ReplaceAll(filepath.ToSlash(abs), "/", "__") + ".lock" }
	hashed, err := paths.HomePathFor(filepath.Join(work, "hashed", "settings.json"))
	require.NoError(t, err)

	return sidecarFixture{
		orphanRecorded:   recorded(gone),
		inUseRecorded:    recorded(present),
		unreapableHashed: empty(filepath.Base(hashed)),
		orphanLegacy:     empty(legacy(filepath.Join(work, "old", "opencode.json"))),
		inUseLegacy:      empty(legacy(present)),
		ambiguousLegacy:  empty(legacy(filepath.Join(work, "_edge", "x.json"))),
	}
}

func assertExist(t *testing.T, want map[string]bool) {
	t.Helper()
	for p, exists := range want {
		_, err := os.Stat(p)
		if exists {
			assert.NoError(t, err, "%s must survive", filepath.Base(p))
		} else {
			assert.ErrorIs(t, err, os.ErrNotExist, "%s must be reaped", filepath.Base(p))
		}
	}
}

// Without Apply the step only reports: every sidecar is still there, and the
// report sorts them as --yes would act on them.
func TestReapLockSidecars_WithoutApplyRemovesNothing(t *testing.T) {
	f := newSidecarFixture(t)

	rep, err := ReapLockSidecars(context.Background(), LockSidecarRequest{Gate: currentGate()})
	require.NoError(t, err)

	want := map[string]bool{}
	for _, p := range f.all() {
		want[p] = true
	}
	assertExist(t, want)
	assert.Equal(t, 2, rep.Orphaned)
	assert.Equal(t, 2, rep.InUse)
	assert.Equal(t, 2, rep.Unreapable)
	assert.Zero(t, rep.Reaped)
	assert.Empty(t, rep.Refused)
}

// With Apply the step reaps exactly the sidecars whose guarded path is shown
// gone — by its record, or by an unambiguous legacy name — and nothing else.
func TestReapLockSidecars_ReapsOnlyWhatOutlivedItsFile(t *testing.T) {
	f := newSidecarFixture(t)

	rep, err := ReapLockSidecars(context.Background(), LockSidecarRequest{Apply: true, Gate: currentGate()})
	require.NoError(t, err)

	assertExist(t, map[string]bool{
		f.orphanRecorded:   false,
		f.orphanLegacy:     false,
		f.inUseRecorded:    true,
		f.inUseLegacy:      true,
		f.unreapableHashed: true,
		f.ambiguousLegacy:  true,
	})
	assert.Equal(t, 2, rep.Reaped)
	assert.Zero(t, rep.Busy)
}

// A candidate some process holds is left, and counted as left.
func TestReapLockSidecars_LeavesAHeldCandidate(t *testing.T) {
	f := newSidecarFixture(t)
	held, err := safefs.New().Locks.Lock(f.orphanRecorded)
	require.NoError(t, err)
	defer func() { _ = held.Unlock() }()

	rep, err := ReapLockSidecars(context.Background(), LockSidecarRequest{Apply: true, Gate: currentGate()})
	require.NoError(t, err)

	assertExist(t, map[string]bool{f.orphanRecorded: true, f.orphanLegacy: false})
	assert.True(t, held.Current(), "the holder's lock file was replaced")
	assert.Equal(t, 1, rep.Reaped)
	assert.Equal(t, 1, rep.Busy)
}

// A refusing gate keeps every sidecar, Apply or not, and the report carries
// why and what to do.
func TestReapLockSidecars_ARefusingGateReapsNothing(t *testing.T) {
	f := newSidecarFixture(t)
	gate := currentGate()
	gate.Containers = func(context.Context) ([]string, error) { return []string{"ctxloom-iso-run"}, nil }

	rep, err := ReapLockSidecars(context.Background(), LockSidecarRequest{Apply: true, Gate: gate})
	require.NoError(t, err)

	assertExist(t, map[string]bool{f.orphanRecorded: true, f.orphanLegacy: true})
	assert.Zero(t, rep.Reaped)
	assert.Contains(t, rep.Refused, "ctxloom-iso-run")
	assert.NotEmpty(t, rep.Remedy)
}

// With no candidate there is nothing to gate: the gate is never consulted.
func TestReapLockSidecars_NoCandidateNeverConsultsTheGate(t *testing.T) {
	testsupport.Isolate(t)
	asked := false
	gate := currentGate()
	gate.Processes = func() ([]RunningProcess, error) { asked = true; return nil, nil }

	rep, err := ReapLockSidecars(context.Background(), LockSidecarRequest{Apply: true, Gate: gate})
	require.NoError(t, err)
	assert.False(t, asked)
	assert.Zero(t, rep.Orphaned)
}

// A legacy name's "__" may have been two underscores of a component's own
// name rather than a separator; a sidecar is in use when ANY reading names
// something that exists.
func TestLegacyReadingExists_TriesALiteralDoubleUnderscore(t *testing.T) {
	work := t.TempDir()
	real := filepath.Join(work, "a__b", "file")
	require.NoError(t, os.MkdirAll(filepath.Dir(real), 0o755))
	require.NoError(t, os.WriteFile(real, nil, 0o644))

	parts, ok := legacyLockStem(strings.ReplaceAll(filepath.ToSlash(real), "/", "__") + ".lock")
	require.True(t, ok)
	assert.True(t, legacyReadingExists("/", parts), "the a__b reading exists")

	parts, ok = legacyLockStem(strings.ReplaceAll(filepath.ToSlash(filepath.Join(work, "a__c", "file")), "/", "__") + ".lock")
	require.True(t, ok)
	assert.False(t, legacyReadingExists("/", parts), "no reading of a__c/file exists")
}

// Only a name that decodes without a guess is a legacy name.
func TestLegacyLockStem_RefusesWhatCannotBeDecoded(t *testing.T) {
	for name, file := range map[string]string{
		"hashed":           "__tmp__x__.mcp.json__0123456789abcdef.lock",
		"relative":         "tmp__x.lock",
		"triple":           "__tmp___x.lock",
		"trailing sep":     "__tmp__x__.lock",
		"no lock suffix":   "__tmp__x",
		"root only":        "__.lock",
		"fixed delivery":   "static-delivery.lock",
		"empty component":  "____tmp.lock",
		"leading _ in seg": "__tmp___x__y.lock",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := legacyLockStem(file)
			assert.False(t, ok)
		})
	}
	parts, ok := legacyLockStem("__tmp__x__.mcp.json.lock")
	require.True(t, ok)
	assert.Equal(t, []string{"tmp", "x", ".mcp.json"}, parts)
}

// The gate refuses on each thing that could be taking these locks under an
// older protocol, and on each absence of evidence; it allows only when every
// source has answered "nothing older".
func TestLockReapGate_RefusesAnythingThatMayBeOlder(t *testing.T) {
	versionOf := func(body string) func(RunningProcess) ([]byte, error) {
		return func(RunningProcess) ([]byte, error) { return []byte(body), nil }
	}
	withProcs := func(g LockReapGate, extra ...RunningProcess) LockReapGate {
		g.Processes = func() ([]RunningProcess, error) {
			return append([]RunningProcess{{PID: gateSelf, Comm: "ctxloom", Exe: "/usr/local/bin/ctxloom"}}, extra...), nil
		}
		return g
	}
	for name, tc := range map[string]struct {
		gate LockReapGate
		want string
	}{
		"a binary without lock_protocol": {
			gate: func() LockReapGate {
				g := withProcs(currentGate(), RunningProcess{PID: 77, Comm: "ltk", Exe: "/usr/bin/ltk"})
				g.Version = versionOf(`{"name":"ltk","version":"v0.6"}`)
				return g
			}(),
			want: "ltk (pid 77)",
		},
		"a lower lock_protocol": {
			gate: func() LockReapGate {
				g := withProcs(currentGate(), RunningProcess{PID: 78, Comm: "taskloom", Exe: "/usr/bin/taskloom"})
				g.Version = versionOf(`{"name":"taskloom","version":"v0.7","lock_protocol":1}`)
				return g
			}(),
			want: "lock_protocol 1",
		},
		"a replaced binary": {
			gate: withProcs(currentGate(), RunningProcess{PID: 79, Comm: "ctxloom", Exe: "/usr/local/bin/ctxloom (deleted)"}),
			want: "ctxloom (pid 79)",
		},
		"an unreadable binary": {
			gate: withProcs(currentGate(), RunningProcess{PID: 80, Comm: "ctxloom", ExeErr: syscall.EACCES}),
			want: "ctxloom (pid 80)",
		},
		"a binary that cannot be asked": {
			gate: func() LockReapGate {
				g := withProcs(currentGate(), RunningProcess{PID: 81, Comm: "ltk", Exe: "/usr/bin/ltk"})
				g.Version = func(RunningProcess) ([]byte, error) { return nil, errors.New("exit status 1") }
				return g
			}(),
			want: "ltk (pid 81)",
		},
		"a family binary started through a link of another name": {
			gate: func() LockReapGate {
				g := withProcs(currentGate(), RunningProcess{PID: 82, Comm: "ctxloom", Exe: "/opt/ctxloom/v0.6/ctxloom-linux-amd64"})
				g.Version = versionOf(`{"name":"ctxloom","version":"v0.6"}`)
				return g
			}(),
			want: "pid 82",
		},
		"a running agent container": {
			gate: func() LockReapGate {
				g := currentGate()
				g.Containers = func(context.Context) ([]string, error) { return []string{"ctxloom-iso-agent-1"}, nil }
				return g
			}(),
			want: "ctxloom-iso-agent-1",
		},
		"a runtime that cannot list its containers": {
			gate: func() LockReapGate {
				g := currentGate()
				g.Containers = func(context.Context) ([]string, error) { return nil, errors.New("daemon down") }
				return g
			}(),
			want: "daemon down",
		},
		"a platform without /proc": {
			gate: func() LockReapGate {
				g := currentGate()
				g.Processes = func() ([]RunningProcess, error) { return nil, ErrProcessListUnavailable }
				return g
			}(),
			want: "unavailable",
		},
		"a listing that does not show this process": {
			gate: func() LockReapGate {
				g := currentGate()
				g.Processes = func() ([]RunningProcess, error) { return nil, nil }
				return g
			}(),
			want: "does not show this process",
		},
	} {
		t.Run(name, func(t *testing.T) {
			why, remedy := tc.gate.refusal(context.Background())
			assert.Contains(t, why, tc.want)
			assert.NotEmpty(t, remedy)
		})
	}
}

// A replaced binary refuses on the exe alone: whatever now sits at its path
// is not what the process runs, so its answer would not be the process's.
func TestLockReapGate_NeverAsksAReplacedBinary(t *testing.T) {
	g := currentGate()
	g.Processes = func() ([]RunningProcess, error) {
		return []RunningProcess{{PID: gateSelf, Comm: "ctxloom"}, {PID: 9, Comm: "ltk", Exe: "/usr/bin/ltk (deleted)"}}, nil
	}
	g.Version = func(RunningProcess) ([]byte, error) {
		t.Fatal("a replaced binary was asked its version")
		return nil, nil
	}
	why, _ := g.refusal(context.Background())
	assert.Contains(t, why, "ltk (pid 9)")
}

// Every source answering "nothing older" allows the reap: current family
// binaries, other programs, one that exited before it could be asked, and
// this process itself — never asked, however it looks.
func TestLockReapGate_AllowsWhenEverythingIsCurrent(t *testing.T) {
	g := currentGate()
	g.Processes = func() ([]RunningProcess, error) {
		return []RunningProcess{
			{PID: gateSelf, Comm: "ctxloom", Exe: "/old/ctxloom (deleted)"},
			{PID: 10, Comm: "ltk", Exe: "/usr/local/bin/ltk"},
			{PID: 11, Comm: "taskloom", Exe: "/usr/local/bin/taskloom"},
			{PID: 12, Comm: "bash", Exe: "/usr/bin/bash"},
			{PID: 13, Comm: "kworker/0:1", ExeErr: os.ErrNotExist},
			{PID: 14, Comm: "ltk", Exe: "/usr/local/bin/ltk"},
		}, nil
	}
	var asked []int
	g.Version = func(p RunningProcess) ([]byte, error) {
		asked = append(asked, p.PID)
		if p.PID == 14 {
			return nil, ErrProcessGone
		}
		return []byte(`{"name":"x","version":"v1","lock_protocol":2}`), nil
	}

	why, remedy := g.refusal(context.Background())
	assert.Empty(t, why)
	assert.Empty(t, remedy)
	assert.ElementsMatch(t, []int{10, 11, 14}, asked)
}
