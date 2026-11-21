#!/usr/bin/env python3
"""What the terminal wake adds to a primary session's PostToolUse hook
(owner ruling 2026-11-18: under 20 ms at p95, nothing pending).

Run by `just hook-latency [base] [calls]`, which builds the test-support
binary of the base (before the wake) and of the tree (with it) and passes
both. For each, in a temporary home of its own: a project, `ctxloom init`, a
daemon (`daemon serve`; two versions cannot share one, a newer command stops
an older daemon), and a seeded primary session (`test seed-session`, no
owner, so the wake's hooks ask for its messages). Then each binary's hooks
`hook claude-code execute <PostToolUse|UserPromptSubmit>`, its session
named (`CTXLOOM_SESSION`, so both deliver the call's capture fact as
before), are fired CALLS times each, the two binaries interleaved so the
machine's load falls on both alike. The difference is what the wake added
(a prompt's `GetSession`; a tool call asks for none).

p50, p95 and the mean of each, and of the pairwise difference, in
milliseconds, on stdout; exit 1 when the head's PostToolUse p95 is 20 ms or
more over the base's (the prompt's is reported, not budgeted). Not a gate:
a measurement moves with the machine's load.
"""

import json
import os
import shutil
import statistics
import subprocess
import sys
import tempfile
import time

BUDGET_MS = 20.0
HARP = "amber-quiet-heron"
# Each hook measured, with its payload; the budget is PostToolUse's.
HOOKS = {
    "PostToolUse": {
        "session_id": "9d764b45-a56a-48d7-ac9d-c2a16c5669f1",
        "hook_event_name": "PostToolUse",
        "tool_name": "Bash",
        "tool_input": {"command": "true"},
        "tool_response": "ok",
    },
    "UserPromptSubmit": {
        "session_id": "9d764b45-a56a-48d7-ac9d-c2a16c5669f1",
        "hook_event_name": "UserPromptSubmit",
        "prompt": "go on",
    },
}
BUDGETED = "PostToolUse"


def run(binary, env, cwd, *args, stdin=None, check=True):
    done = subprocess.run(
        [binary, *args],
        env=env,
        cwd=cwd,
        input=stdin,
        capture_output=True,
        text=True,
        timeout=60,
    )
    if check and done.returncode != 0:
        sys.exit(f"hook-latency: `ctxloom {' '.join(args)}` exited "
                 f"{done.returncode}: {done.stderr.strip()}")
    return done


def serving(binary, env, cwd):
    status = run(binary, env, cwd, "daemon", "status", "--format", "json", check=False)
    try:
        return "pid" in json.loads(status.stdout or "{}")
    except json.JSONDecodeError:
        return False


def percentile(values, p):
    ordered = sorted(values)
    rank = max(0, min(len(ordered) - 1, round(p / 100 * len(ordered) + 0.5) - 1))
    return ordered[rank]


class Home:
    """A binary's temporary home: a project, a daemon, a seeded primary."""

    def __init__(self, binary, top, name):
        self.binary = binary
        home = os.path.join(top, name)
        self.project = os.path.join(home, "project")
        os.makedirs(self.project)
        self.env = {"HOME": home, "USERPROFILE": home, "TMPDIR": top,
                    "PATH": os.environ["PATH"]}
        self.daemon = None

    def run(self, *args, **kw):
        return run(self.binary, self.env, self.project, *args, **kw)

    def start(self):
        subprocess.run(["git", "init", "-q"], cwd=self.project, check=True, env=self.env)
        self.run("init")
        self.daemon = subprocess.Popen(
            [self.binary, "daemon", "serve", "--idle-seconds", "900"],
            env=self.env, cwd=self.project,
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        deadline = time.monotonic() + 30
        while not serving(self.binary, self.env, self.project):
            if time.monotonic() > deadline or self.daemon.poll() is not None:
                sys.exit(f"hook-latency: {self.binary}'s daemon never served")
            time.sleep(0.05)
        self.run("test", "seed-session", HARP)
        session = self.run("test", "session-name", HARP).stdout.strip()
        self.hook_env = dict(self.env, CTXLOOM_SESSION=session)
        for hook in HOOKS:
            self.fire(hook)  # unmeasured: the daemon's first read warms it

    def fire(self, hook):
        payload = json.dumps(dict(HOOKS[hook], cwd=self.project))
        started = time.perf_counter()
        run(self.binary, self.hook_env, self.project,
            "hook", "claude-code", "execute", hook, stdin=payload)
        return (time.perf_counter() - started) * 1000

    def stop(self):
        if self.daemon is None:
            return
        self.run("daemon", "stop", check=False)
        try:
            self.daemon.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.daemon.kill()


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: hook-latency.py <base ctxloom> <wake ctxloom> <calls>")
    before, after = (os.path.abspath(b) for b in sys.argv[1:3])
    calls = int(sys.argv[3])
    top = tempfile.mkdtemp(prefix="ctxloom-hook-latency-")
    homes = [Home(before, top, "base"), Home(after, top, "wake")]
    try:
        for home in homes:
            home.start()
        over = {}
        for hook in HOOKS:
            times = ([], [])
            for _ in range(calls):
                for home, kept in zip(homes, times):
                    kept.append(home.fire(hook))
            added = [h - b for b, h in zip(*times)]
            print(f"{hook} hook, nothing pending, {calls} calls each, ms")
            print(f"{'':20} {'p50':>8} {'p95':>8} {'mean':>8}")
            for name, values in (("base", times[0]), ("head", times[1]),
                                 ("added, pairwise", added)):
                print(f"{name:20} {percentile(values, 50):8.2f} "
                      f"{percentile(values, 95):8.2f} {statistics.fmean(values):8.2f}")
            over[hook] = percentile(times[1], 95) - percentile(times[0], 95)
            print(f"p95 added: {over[hook]:.2f} ms\n")
        print(f"{BUDGETED}'s p95 added: {over[BUDGETED]:.2f} ms (budget {BUDGET_MS:.0f} ms)")
        return 0 if over[BUDGETED] < BUDGET_MS else 1
    finally:
        for home in homes:
            home.stop()
        shutil.rmtree(top, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
