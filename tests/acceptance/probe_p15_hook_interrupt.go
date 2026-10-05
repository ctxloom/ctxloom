// Package acceptance: P15, HOOK INTERRUPT — the capability ladder's rung for
// what claude does to a hook it is waiting on when ctxloom interrupts the
// turn (conformance cell I1).
//
// UNTAGGED, like probe_p12_permission_hook.go: the cell is @live and paid, so
// the verdict, the fixture's wire and the liveness read below are the only
// part of this rung a hermetic test can execute
// (probe_p15_hook_interrupt_test.go). The godog plumbing lives in
// steps_p15_hook_interrupt.go behind the acceptance tag.
//
// WHAT THIS RUNG PINS. An interrupted turn is an ordinary boundary for the
// runner (EngineHost.runTurn): the turn's approval slots are cancelled and
// the run lives. That is sound only if the hook claude was waiting on DIES
// with the turn — an orphaned approval hook goes on holding a request the
// runner has already cancelled, and goes on running code nobody is watching.
// The cell blocks claude on a PermissionRequest hook that holds far longer
// than the cell waits, interrupts claude the way the driver does
// (procsig.Interrupt to a process-group leader, then a kill after the grace
// spawnChatTransportGrace gives it), and reads the hook's own process from
// /proc once claude has exited.
//
// Whether claude writes a result frame on its way out is MEASURED, not
// required: the driver relays one if it comes (relayTurn) and ends the turn
// on the interrupt either way, so the evidence line records it for the
// registry and the verdict does not depend on it.
package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// p15Family is this rung's name in a skip line, a failure message and the
// evidence line.
const p15Family = "hook-interrupt"

// p15Grace is how long claude gets to exit after the interrupt before the
// cell kills it — the grace claude's driver gives an interrupted turn
// (turnInterruptGrace in internal/engines/claude), so the cell judges claude
// by the bound production holds it to.
const p15Grace = 10 * time.Second

// p15HookStartWait bounds the wait for the hook to start blocking: the turn
// must reach its gated call and claude must run the hook.
const p15HookStartWait = 90 * time.Second

// p15HookHold is how long the hook sleeps before it would answer. It outlasts
// every wait the cell makes, so a marker on disk means the hook was never
// actually blocked when the interrupt landed.
const p15HookHold = 150 * time.Second

// p15ReapWait is how long the cell gives a dying hook to leave /proc after
// claude has exited.
const p15ReapWait = 3 * time.Second

// The fixture's file names inside the cell's directory; the rest are P12's.
const (
	p15HookPIDName  = "hook-pid"  // the hook's own pid, written once it holds its input
	p15SleepPIDName = "sleep-pid" // the hook's sleeping child, recorded for the evidence
)

// errP15HookNeverBlocked: the hook wrote no pid before the wait ran out or
// claude exited — nothing was blocked, so nothing was interrupted.
var errP15HookNeverBlocked = errors.New(p15Family + ": the PermissionRequest hook never started blocking")

// p15HookScript renders the hook: capture stdin, record its pid (atomically,
// so a reader never sees a partial one), hold in a backgrounded sleep whose
// pid it also records, then — only if it outlives the hold — stamp the marker
// and answer allow.
func p15HookScript(dir string) (string, error) {
	q := func(name string) string { return p12ShellQuote(dir + "/" + name) }
	out, err := json.Marshal(p12HookOutput{HookSpecificOutput: p12HookSpecific{
		HookEventName: p12HookEvent,
		Decision:      p12HookDecision{Behavior: p12Allow},
	}})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#!/bin/sh\ncat > %s\necho $$ > %s.tmp && mv %s.tmp %s\nsleep %d &\necho $! > %s\nwait\n: > %s\nprintf '%%s\\n' %s\n",
		q(p12HookInputName), q(p15HookPIDName), q(p15HookPIDName), q(p15HookPIDName),
		int(p15HookHold/time.Second), q(p15SleepPIDName), q(p12MarkerName), p12ShellQuote(string(out))), nil
}

// p15Args is the turn's argv after the binary: the driver's stream-json
// shape (streamJSONDriver.argv), the hook's settings, the cheap model.
func p15Args(settingsPath string) []string {
	return []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--settings", settingsPath, "--model", liveClaudeModel}
}

// p15UserMessage is the one stdin line that carries the prompt, in the shape
// the driver writes it (writeUserMessage).
func p15UserMessage(prompt string) ([]byte, error) {
	var m struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	m.Type, m.Message.Role, m.Message.Content = "user", "user", prompt
	b, err := json.Marshal(m)
	return append(b, '\n'), err
}

// p15ProcessAlive reports whether pid is a live process: present in /proc and
// not a zombie. A process gone from /proc, or one that has died and awaits
// its reaper, is not alive.
func p15ProcessAlive(pid int) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// stat is "pid (comm) state ...", and comm may itself contain ") ".
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 || i+2 >= len(raw) {
		return false, fmt.Errorf("%s: unparseable /proc/%d/stat %q", p15Family, pid, raw)
	}
	switch raw[i+2] {
	case 'Z', 'X':
		return false, nil
	}
	return true, nil
}

// p15ReadPID reads a pid file the hook wrote.
func p15ReadPID(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

// p15Ending reports whether the interrupted turn's stdout carried a result
// frame, and its subtype. A line the kill cut short ends the read.
func p15Ending(stdout string) (sawResult bool, subtype string) {
	for _, line := range strings.Split(stdout, "\n") {
		var f p12Frame
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &f) != nil {
			continue
		}
		if f.Type == "result" {
			sawResult, subtype = true, f.Subtype
		}
	}
	return sawResult, subtype
}

// p15Outcome is everything a P15 verdict may look at.
type p15Outcome struct {
	Cell    probeCellID
	Started bool
	Run     probeRun
	// HookPID is the blocked hook's own process; HookStartErr is why the
	// cell never saw it start blocking.
	HookPID      int
	HookStartErr error
	// ExitedOnInterrupt: claude exited within p15Grace of the interrupt;
	// ExitAfter is how long it took (or the grace, when it was killed).
	ExitedOnInterrupt bool
	ExitAfter         time.Duration
	// HookAlive: the hook's process was still alive p15ReapWait after claude
	// exited; HookAliveErr is a liveness read that failed.
	HookAlive    bool
	HookAliveErr error
	// SleepAlive is the same read for the hook's sleeping child — evidence
	// only: ctxloom's own hook is one process with no children.
	SleepAlive bool
	// MarkerExists: the hook outlived its whole hold and answered.
	MarkerExists bool
}

// The shapes this rung adds; shapeNotAttempted and shapeHookNotFired are
// P12's.
const (
	// shapeInterruptIgnored: claude was still running p15Grace after the
	// interrupt, so the driver's kill — not claude — ends such a turn.
	shapeInterruptIgnored probeShape = "INTERRUPT-IGNORED failure"
	// shapeHookOrphaned: claude exited and the hook it was waiting on did
	// not, so an interrupted turn leaves its approval hook running.
	shapeHookOrphaned probeShape = "HOOK-ORPHANED failure"
)

func (o p15Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p15Family, Cell: o.Cell, Channel: channelHookProcess}
}

// summary is the one-line measurement the evidence line prints and the
// registry's Reason quotes.
func (o p15Outcome) summary() string {
	saw, subtype := p15Ending(o.Run.Stdout)
	return fmt.Sprintf("hookPID=%d exitedOnInterrupt=%t exitAfter=%s exit=%d runErr=%v resultFrame=%t subtype=%q hookAlive=%t sleepAlive=%t marker=%t",
		o.HookPID, o.ExitedOnInterrupt, o.ExitAfter.Round(time.Millisecond), o.Run.ExitCode, o.Run.Err, saw, subtype, o.HookAlive, o.SleepAlive, o.MarkerExists)
}

func (o p15Outcome) evidence() string {
	return fmt.Sprintf("\n%s hookStartErr=%v hookAliveErr=%v\nstdout:\n%s\nstderr:\n%s",
		o.summary(), o.HookStartErr, o.HookAliveErr, o.Run.Stdout, o.Run.Stderr)
}

// p15Assert judges the cell. claude's own exit status is not judged: an
// interrupted process may exit in failure, and the driver treats that turn
// as interrupted, not dead.
func p15Assert(o p15Outcome) error {
	v := o.verdict()
	if !o.Started {
		return v.fail(shapeRunFailed, "the claude run never started", o.evidence())
	}
	if o.HookStartErr != nil {
		s, _ := p12Decode(o.Run.Stdout)
		if len(s.GatedCalls) == 0 {
			return v.fail(shapeNotAttempted, "the model made no "+p12GatedTool+" tool_use, so no hook blocked and nothing was interrupted", o.evidence())
		}
		return v.fail(shapeHookNotFired, fmt.Sprintf("the gated call was made but the %s hook never started blocking: %v", p12HookEvent, o.HookStartErr), o.evidence())
	}
	if o.MarkerExists {
		return v.fail(shapeRunFailed, fmt.Sprintf("the hook outlived its %s hold and answered, so it was not blocked when the interrupt landed", p15HookHold), o.evidence())
	}
	if !o.ExitedOnInterrupt {
		return v.fail(shapeInterruptIgnored, fmt.Sprintf("claude was still running %s after SIGINT and had to be killed", p15Grace), o.evidence())
	}
	if o.HookAliveErr != nil {
		return v.fail(shapeRunFailed, fmt.Sprintf("the hook's liveness could not be read: %v", o.HookAliveErr), o.evidence())
	}
	if o.HookAlive {
		return v.fail(shapeHookOrphaned, fmt.Sprintf("claude exited on SIGINT but its blocked %s hook (pid %d) was still alive %s later", p12HookEvent, o.HookPID, p15ReapWait), o.evidence())
	}
	return nil
}
