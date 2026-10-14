package isolation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// Per-container resource caps. Without them one delegated agent's runaway
// build (a `cargo mutants` sweep) drove the HOST into OOM thrash, and the
// coordinator's heartbeat watchdog — starved along with everything else —
// then tore down every run, the human's own session included. A cap moves
// that failure inside the one container: the kernel OOM-kills within its
// cgroup, and the host, the coordinator and the other agents keep running.
const (
	// defaultMemoryShareDivisor makes the default memory cap 1/2 of the
	// container host's physical memory. Half is chosen so that ONE runaway
	// agent always leaves the other half to the host, its desktop session and
	// the coordinator, which is the failure observed. It deliberately does
	// NOT divide by the number of agents: that number is not known at
	// launch, and a 1/N share would starve the common one-agent run. The
	// consequence, stated: the cap bounds each container, not their sum, so
	// two agents running away at the same time can still exhaust the host
	// together. Bounding the aggregate needs a shared parent cgroup across
	// containers, which this cap does not attempt.
	defaultMemoryShareDivisor = 2
	// defaultMemoryFloor is the least the default gives a container (2 GiB):
	// an engine CLI plus an ordinary build needs on the order of a gigabyte,
	// so on a small host half would starve the agent rather than protect
	// anything. The floor never exceeds the host's own memory.
	defaultMemoryFloor = int64(2) << 30
	// defaultPidsLimit is the default process/thread cap. A heavy parallel
	// build (cargo/rustc jobs × threads, test binaries with one thread per
	// core) on a large machine stays in the low thousands, so 4096 leaves
	// room for it while stopping a fork bomb, which would otherwise exhaust
	// the launching user's process limit — shared, under a rootless runtime,
	// with the human's own session. Podman's own default (containers.conf
	// pids_limit) is 2048; docker has none. One value for both runtimes.
	defaultPidsLimit = 4096
)

// limitSupport is what a runtime's engine reported, at selection, about the
// caps it can enforce — read off the SAME `info` call that decided its
// ownership (no second probe). The zero value enforces nothing and knows no
// host memory, which is what a runtime that reported nothing gets.
type limitSupport struct {
	// hostMemory is the daemon host's physical memory in bytes (the VM's,
	// under Docker Desktop or a podman machine — the machine the cap is
	// enforced on); 0 when not reported.
	hostMemory int64
	memory     bool // --memory is enforced
	swap       bool // --memory-swap is enforced (swap accounting)
	pids       bool // --pids-limit is enforced
	// unenforced says why no cap can be enforced (empty when some can), for
	// the one-time warning.
	unenforced string
}

// resourceLimits are one run's resolved caps, rendered as run flags; 0 is no
// flag. memorySwap is memory's own value wherever swap accounting exists, so
// swap cannot stretch the memory cap.
type resourceLimits struct {
	memory     int64
	memorySwap int64
	pids       int64
}

// args renders the caps as `run` flags.
func (l resourceLimits) args() []string {
	var args []string
	if l.memory > 0 {
		args = append(args, "--memory="+strconv.FormatInt(l.memory, 10))
	}
	if l.memorySwap > 0 {
		args = append(args, "--memory-swap="+strconv.FormatInt(l.memorySwap, 10))
	}
	if l.pids > 0 {
		args = append(args, "--pids-limit="+strconv.FormatInt(l.pids, 10))
	}
	return args
}

// defaultMemoryLimit is the default memory cap on a host with hostMemory
// bytes: hostMemory/defaultMemoryShareDivisor, raised to defaultMemoryFloor,
// never above hostMemory. 0 (no cap) when the host's memory is unknown.
func defaultMemoryLimit(hostMemory int64) int64 {
	if hostMemory <= 0 {
		return 0
	}
	return min(max(hostMemory/defaultMemoryShareDivisor, defaultMemoryFloor), hostMemory)
}

// resolveLimits turns the configured caps into a run's flags against what
// the runtime enforces: an unset cap takes its default (memory derived from
// the daemon host), an explicit one is taken as written, 0/unlimited is no
// flag. A cap the runtime cannot enforce is dropped with a one-time warning —
// a rootless podman on cgroup v1 refuses a run given one outright, and an
// uncapped agent beats no agent.
//
// This is the seam for a request from another source (a devcontainer's
// hostRequirements): it arrives as a config.ContainerLimits like config's,
// so nothing here changes with where the request came from.
func resolveLimits(want config.ContainerLimits, sup limitSupport) resourceLimits {
	var out resourceLimits
	memory := capValue(want.Memory, defaultMemoryLimit(sup.hostMemory))
	if want.Memory.Kind == config.LimitDefault && memory == 0 {
		clidiag.WarnRemedyOnce("ctxloom", "set isolation_memory_limit to a size (e.g. 8g) to cap agent containers",
			"the container runtime did not report its host's memory, so agent containers run without the default memory cap")
	}
	pids := capValue(want.PIDs, defaultPidsLimit)
	if (memory > 0 && !sup.memory) || (pids > 0 && !sup.pids) {
		why := sup.unenforced
		if why == "" {
			why = "the runtime reports no cgroup control for it"
		}
		clidiag.WarnRemedyOnce("ctxloom", "run a rootful runtime, or a rootless one on cgroup v2 with the memory and pids controllers delegated, to enforce the caps",
			"agent containers run without some resource caps (isolation_memory_limit / isolation_pids_limit): %s", why)
	}
	if memory > 0 && sup.memory {
		out.memory = memory
		if sup.swap {
			out.memorySwap = memory
		}
	}
	if pids > 0 && sup.pids {
		out.pids = pids
	}
	return out
}

// capValue is one cap's value: def when unset, 0 when off, else as written.
func capValue(l config.ContainerLimit, def int64) int64 {
	switch l.Kind {
	case config.LimitFixed:
		return l.Value
	case config.LimitUnlimited:
		return 0
	default:
		return def
	}
}

// oomQueryTimeout bounds the after-exit OOM-event query: it is a lookup in
// the daemon's event log, and a wedged daemon must not hold the runner's
// exit report hostage.
const oomQueryTimeout = 5 * time.Second

// sigkillStatus is the exit status of a process killed by SIGKILL (128+9),
// which is how the kernel's OOM killer ends one.
const sigkillStatus = 137

// noteOOMKill adds to a failed capped run's exit error whether the memory cap
// killed it, read at the point the exit is observed. `inspect .State.OOMKilled`
// cannot answer then: the run is `--rm`, and the runtime has removed the
// container before its `run` CLI returns. Docker's daemon keeps an `oom`
// event in its event log past the removal, so that is asked instead (since
// the run's start). Podman logs no OOM event, so there the kill is only
// SUGGESTED, for a SIGKILL exit under a cap. The exit status (and the
// *exitstatus.Error carrying it) is preserved; only the text grows, which is
// what the run's end record renders.
func noteOOMKill(rt Runtime, name string, limits resourceLimits, since time.Time, err error) error {
	if err == nil || limits.memory <= 0 {
		return err
	}
	limit := humanBytes(limits.memory)
	if args := rt.oomEventArgs(name, since, time.Now()); args != nil {
		ctx, cancel := context.WithTimeout(context.Background(), oomQueryTimeout)
		defer cancel()
		out, qerr := probeExec(ctx, rt.Binary(), args)
		if qerr != nil || !strings.Contains(out, "oom") {
			return err
		}
		return withNote(err, fmt.Sprintf("OOM-killed (memory limit %s)", limit))
	}
	var es *exitstatus.Error
	if errors.As(err, &es) && es.ExitStatus() == sigkillStatus {
		return withNote(err, fmt.Sprintf("possibly OOM-killed (memory limit %s; %s records no OOM event to confirm it)", limit, rt.Name()))
	}
	return err
}

// withNote appends note to err's text, keeping its classified exit status.
func withNote(err error, note string) error {
	noted := fmt.Errorf("%w; %s", err, note)
	var es *exitstatus.Error
	if errors.As(err, &es) {
		return &exitstatus.Error{Code: es.Code, Err: noted}
	}
	return noted
}

// humanBytes renders a byte count in GiB (one decimal) from 1 GiB up, else
// in whole MiB.
func humanBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%dMiB", n>>20)
}

// unixNanoStamp renders t as docker's `seconds.nanoseconds` timestamp.
func unixNanoStamp(t time.Time) string {
	return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
}
