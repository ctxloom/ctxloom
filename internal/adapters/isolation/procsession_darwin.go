//go:build darwin

package isolation

import "golang.org/x/sys/unix"

// killSession SIGKILLs every process in session sid — the darwin arm of the
// sweep procsession_linux.go runs over /proc. A runner spawned via
// isolateRunner leads its own session (sid == its pid), and a descendant that
// moves itself into another process group still carries that session id, so
// selecting by SESSION reaches the grandchild a process-group kill would miss.
// The one escape, on every unix, is a descendant that calls setsid(2) itself.
//
// darwin has no pidfd to pin a process between deciding and signalling, so
// this signals GROUPS rather than pids: XNU refuses to hand out a pid that is
// still in use as a live process-group or session id (forkproc), so a pgid
// cannot be recycled while the group has members. The residual window — a
// group empties between enumeration and kill and a newborn both reuses the
// number and leads a new group — is far narrower than a per-pid race.
//
// Best-effort, like the Linux arm: a table that cannot be read or a group
// already gone is not an error. The sweep repeats until the session has no
// live member, so a process forked mid-sweep is caught by the next pass;
// the pass bound keeps a runaway forker from pinning the caller forever.
func killSession(sid int) {
	if sid <= 1 {
		return // never launchd's session, never "every process" (kill(-1))
	}
	for range sessionSweepPasses {
		groups := sessionGroups(sid)
		if len(groups) == 0 {
			return
		}
		for _, pgid := range groups {
			_ = unix.Kill(-pgid, unix.SIGKILL)
		}
	}
}

// sessionSweepPasses bounds killSession's repeat-until-empty loop.
const sessionSweepPasses = 8

// szomb is darwin's SZOMB process state (sys/proc.h), which x/sys/unix does
// not export. A zombie is already dead and only awaits its parent's reap;
// counting it as a member would keep every pass busy with nothing to kill.
const szomb = 5

// sessionGroups returns the distinct process groups whose live members
// belong to session sid. Membership is read with getsid(2), which XNU answers
// for any existing pid without a permission check (kern_prot.c); the group
// comes from the same kern.proc.all snapshot. POSIX process groups never
// span sessions, so killing these groups kills exactly the session.
func sessionGroups(sid int) []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	seen := make(map[int]bool)
	var groups []int
	for i := range procs {
		p := &procs[i]
		if p.Proc.P_stat == szomb {
			continue
		}
		if s, err := unix.Getsid(int(p.Proc.P_pid)); err != nil || s != sid {
			continue
		}
		pgid := int(p.Eproc.Pgid)
		if pgid <= 1 || seen[pgid] {
			continue
		}
		seen[pgid] = true
		groups = append(groups, pgid)
	}
	return groups
}
