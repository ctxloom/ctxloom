package agent

import "fmt"

// argvlimit.go refuses a launch whose argv cannot exec, BEFORE exec is
// attempted, so the failure names the payload that caused it.
//
// THE LIMIT: Linux caps a SINGLE argv element at MAX_ARG_STRLEN =
// 32 * PAGE_SIZE bytes, counting the terminating NUL, and does so
// INDEPENDENTLY of the far larger total ARG_MAX (2 MiB on a typical host).
// So one long argument fails at exec while the whole argument list is three
// orders of magnitude inside the total budget. Probed on a 4096-byte-page
// host: a 131071-byte argument execs; 131072 returns E2BIG.
//
// WHY IT MATTERS HERE: codex and kiro carry the run's prompt as an argv
// positional (codex.Codex.buildArgs, kiro.Kiro.buildArgs) — 128 KiB is roughly
// 32k tokens, well within reach of a coordinator handing over a long brief, a
// pasted file, or an assembled context. Without this check the user saw only
// os/exec's own "fork/exec /usr/bin/codex: argument list too long": it names
// neither the prompt nor its length, and it points at the TOTAL argument list,
// which is innocent.
//
// The refusal is the honest failure, not a fallback: shortening the prompt to
// fit would run the turn, answer a question nobody asked, and report success.

// singleArgLimit reports the largest byte length ONE argv element may carry on
// goos with pageSize-byte pages, or 0 when the platform declares no
// per-argument cap — in which case nothing is refused here and a launch that
// is too big for the platform's TOTAL argv budget still surfaces as os/exec's
// own error.
//
// goos and pageSize are parameters rather than runtime.GOOS/os.Getpagesize()
// read inline, so the platform gate is unit-testable (the same shape
// isolation.containerSpawnUnsupportedErr and acp.containerReachBackEnv use).
//
// Only Linux is capped per-argument. macOS limits the total (ARG_MAX) and has
// no MAX_ARG_STRLEN equivalent, so a prompt that Linux refuses can genuinely
// exec there; refusing it anyway would break runs that work.
func singleArgLimit(goos string, pageSize int) int {
	if goos != "linux" || pageSize <= 0 {
		return 0
	}
	// -1: MAX_ARG_STRLEN counts the NUL the kernel appends, so the longest
	// string that fits is one byte short of it.
	return 32*pageSize - 1
}

// checkArgvLimit returns a refusal when any element of args is longer than
// limit bytes, naming the oversized payload and its length. A limit of 0 or
// less disables the check. prompt is the run's prompt content: when it is the
// element that overflowed, the refusal says so in those terms rather than
// pointing at an argv index the user never wrote.
func checkArgvLimit(engine string, args []string, prompt string, limit int) error {
	if limit <= 0 {
		return nil
	}
	for i, arg := range args {
		if len(arg) <= limit {
			continue
		}
		if prompt != "" && arg == prompt {
			return fmt.Errorf("%s: the prompt is %d bytes, and this OS refuses a single command-line argument longer than %d bytes — %s carries the prompt on the command line, so the launch would fail at exec. Shorten the prompt or split the work; ctxloom will not silently send a shortened one",
				engine, len(prompt), limit, engine)
		}
		return fmt.Errorf("%s: command-line argument %d is %d bytes, and this OS refuses a single command-line argument longer than %d bytes, so the launch would fail at exec",
			engine, i, len(arg), limit)
	}
	return nil
}
