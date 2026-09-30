//go:build linux || darwin

package isolation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// procRow is one process-table entry, reduced to the ids that decide whether
// a killSession sweep run from a test binary may consider it.
type procRow struct{ pid, ppid, pgid, sid int }

// descendantsOf is the set of strict descendants of self, walking each row's
// parent chain.
func descendantsOf(rows []procRow, self int) map[int]bool {
	parent := make(map[int]int, len(rows))
	for _, r := range rows {
		parent[r.pid] = r.ppid
	}
	mine := make(map[int]bool)
	for _, r := range rows {
		// The step bound breaks a cycle a pid reused mid-snapshot could form.
		for p, n := parent[r.pid], 0; p > 0 && n < len(rows); p, n = parent[p], n+1 {
			if p == self {
				mine[r.pid] = true
				break
			}
		}
	}
	return mine
}

// groupSafeScope is the set of pids a GROUP-signalling sweep (darwin's
// killSession sends kill(-pgid)) run from self may consider, such that a
// mutant signalling every group in the set reaches only processes this
// binary started:
//
//   - a descendant of self, or a member of a session a descendant leads. The
//     second clause is what keeps an orphan in view on darwin, which has no
//     subreaper: a grandchild whose runner was hard-killed is reparented to
//     launchd, but still carries the runner's session id, and the runner's
//     unreaped zombie keeps that id both a descendant and unrecyclable.
//   - AND its group is led by such a process. A descendant that never left
//     self's own group shares a pgid with self, go test and whatever launched
//     them; signalling that group is how a mutant takes down the harness.
func groupSafeScope(rows []procRow, self int) map[int]bool {
	tree := descendantsOf(rows, self)
	cand := make(map[int]bool)
	for _, r := range rows {
		if tree[r.pid] || tree[r.sid] {
			cand[r.pid] = true
		}
	}
	scope := make(map[int]bool)
	for _, r := range rows {
		if cand[r.pid] && cand[r.pgid] {
			scope[r.pid] = true
		}
	}
	return scope
}

// TestGroupSafeScope pins the rule on a table shaped like a mutation run on
// darwin: an ancestor chain up to launchd, a stranger, a child that never left
// the test binary's group, and an isolated runner whose grandchild was
// orphaned to launchd when the runner was hard-killed.
func TestGroupSafeScope(t *testing.T) {
	const self = 100
	rows := []procRow{
		{pid: 1, ppid: 0, pgid: 1, sid: 1},       // launchd
		{pid: 40, ppid: 1, pgid: 40, sid: 40},    // login shell
		{pid: 50, ppid: 40, pgid: 50, sid: 40},   // go test, leading the job's group
		{pid: self, ppid: 50, pgid: 50, sid: 40}, // the test binary, in go test's group
		{pid: 101, ppid: self, pgid: 50, sid: 40},
		{pid: 102, ppid: self, pgid: 102, sid: 102}, // isolated runner, an unreaped zombie
		{pid: 103, ppid: 1, pgid: 103, sid: 102},    // its grandchild, orphaned to launchd
		{pid: 104, ppid: self, pgid: 104, sid: 40},  // a child that setpgid'd itself
		{pid: 105, ppid: 104, pgid: 104, sid: 40},
		{pid: 200, ppid: 1, pgid: 200, sid: 200}, // a stranger
		{pid: 201, ppid: 40, pgid: 50, sid: 40},  // a stranger sharing go test's group
	}
	require.Equal(t, map[int]bool{102: true, 103: true, 104: true, 105: true}, groupSafeScope(rows, self))
}
