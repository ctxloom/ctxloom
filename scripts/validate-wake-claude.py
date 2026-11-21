#!/usr/bin/env python3
"""The terminal wake's live probe (`just validate-wake-claude`), re-run by the
owner on each claude bump (owner ruling, 2026-11-18). Not part of any gate.

claude's cross-session messaging is undocumented and can change under a
version without notice. ctxloom's terminal wake stands on four facts, which
this probe checks against the INSTALLED claude, interactive, in a pty, in a
throwaway HOME and CLAUDE_CONFIG_DIR:

  1. claude exports CLAUDE_CODE_MESSAGING_SOCKET to a stdio MCP server it
     starts (`ctxloom mcp serve` is one), and says whether
     CLAUDE_CODE_MESSAGING_TOKEN is set (never its value);
  2. a post on that socket from that server, the NDJSON wire Go ctxloom used
     (`{"type":"auth","token":...}` when a token is set, then
     `{"type":"user","message":{"role":"user","content":...}}`), starts a turn
     in an IDLE TUI, its UserPromptSubmit prompt the bare line posted;
  3. (REPORTED, not judged) whether a post from a process that is NOT
     claude's descendant starts a turn: the probe itself (no token), and an
     orphan of the server (double-forked, reparented away from claude)
     holding the token in memory. Go ctxloom measured these refused; claude
     2.1.325 on Linux starts a turn for both ("auth line optional here" in
     its own debug log). The wake does not rely on either answer;
  4. a UserPromptSubmit hook's `additionalContext` reaches the model on a
     woken turn (the one model call: the reply must carry a word only the
     context held).

Model calls: ONE (fact 4), on `--model haiku`. Every other woken prompt is
blocked by the probe's own hook (exit 2), so claude calls no model for it.

Credentials: CLAUDE_CODE_OAUTH_TOKEN when the caller has it set, else a LINK
to the user's own claude login (`<login home>/.claude/.credentials.json`, the
link ctxloom's own launch makes), never copied, printed or logged. The login
home is the passwd entry's (a ctxloom session's HOME is its session home),
or VALIDATE_WAKE_CLAUDE_LOGIN_HOME.

    python3 -I scripts/validate-wake-claude.py [claude binary]

Exit 0 every fact held, 1 a fact failed (the evidence is printed), 2 the probe
could not be set up. Every hex run of 16 or more is redacted from what it
prints.
"""

import fcntl
import json
import os
import pty
import pwd
import re
import secrets
import select
import shutil
import signal
import socket
import struct
import sys
import tempfile
import termios
import threading
import time

SOCKET_VAR = "CLAUDE_CODE_MESSAGING_SOCKET"
TOKEN_VAR = "CLAUDE_CODE_MESSAGING_TOKEN"
BOUND = 60.0
OUTSIDER_WAIT = 20.0
HEX = re.compile(r"[0-9a-fA-F]{16,}")
ANSI = re.compile(r"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[\]P][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-B]")
# What must not reach the probed claude from the caller: a parent claude
# session's marks (a probe run from inside claude would be its child), an
# IDE's, a ctxloom session's, and the places this probe sets itself.
INHERITED = re.compile(
    r"^(CLAUDECODE|CLAUDE_PID|CLAUDE_EFFORT|CLAUDE_CONFIG_DIR|CLAUDE_CODE_(MESSAGING_.*|CHILD_SESSION|ENTRYPOINT|EXECPATH|SESSION_.*|SSE_PORT)"
    r"|CTXLOOM_.*|VSCODE_.*|TERM_PROGRAM.*|GIT_ASKPASS|HOME|TERM|ANTHROPIC_API_KEY|ANTHROPIC_AUTH_TOKEN)$"
)


def redact(text):
    return HEX.sub("<hex>", text)


def wire(token, content):
    """Go ctxloom's messaging wire: the auth line when there is a token, then
    the one user line, each a JSON object on its own line."""
    lines = []
    if token:
        lines.append({"type": "auth", "token": token})
    lines.append({"type": "user", "message": {"role": "user", "content": content}})
    return "".join(json.dumps(line) + "\n" for line in lines).encode()


def post(path, token, content):
    """Connect, write, half-close and read what comes back for a second.
    Answers what happened, never the token."""
    try:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(5)
        s.connect(path)
    except OSError as e:
        return {"connected": False, "error": type(e).__name__ + ": " + str(e.strerror or e)}
    try:
        s.sendall(wire(token, content))
        try:
            s.shutdown(socket.SHUT_WR)
        except OSError:
            pass
        s.settimeout(1.5)
        back = b""
        try:
            while True:
                chunk = s.recv(4096)
                if not chunk:
                    break
                back += chunk
        except (socket.timeout, OSError):
            pass
        return {"connected": True, "written": True, "answer": redact(back.decode(errors="replace"))[:300]}
    except OSError as e:
        return {"connected": True, "written": False, "error": type(e).__name__ + ": " + str(e.strerror or e)}
    finally:
        s.close()


# ---------------------------------------------------------------- the stub --


def stub(work):
    """The stdio MCP server claude starts: records what it was given (names
    and presence only), then posts what the probe asks it to, from the
    request files `<work>/ask/<n>.json` ({"content", "orphan"}), answering in
    `<work>/told/<n>.json`."""
    env_socket = os.environ.get(SOCKET_VAR, "")
    token = os.environ.get(TOKEN_VAR, "")
    facts = {
        "socket_set": bool(env_socket),
        "socket_kind": ("unix path" if env_socket.startswith("/") else ("other" if env_socket else "")),
        "socket": env_socket,
        "token_set": bool(token),
        "messaging_names": sorted(k for k in os.environ if k.startswith("CLAUDE_CODE_MESSAGING")),
        "pid": os.getpid(),
        "ppid": os.getppid(),
    }
    tmp = os.path.join(work, "stub-facts.json.tmp")
    with open(tmp, "w") as f:
        json.dump(facts, f)
    os.replace(tmp, os.path.join(work, "stub-facts.json"))

    def asks():
        ask, told = os.path.join(work, "ask"), os.path.join(work, "told")
        while True:
            for name in sorted(os.listdir(ask)):
                if os.path.exists(os.path.join(told, name)):
                    continue
                with open(os.path.join(ask, name)) as f:
                    req = json.load(f)
                if req.get("orphan"):
                    result = orphan_post(env_socket, token, req["content"])
                else:
                    result = post(env_socket, token, req["content"]) if env_socket else {"connected": False, "error": "no socket"}
                result["poster_pid"] = result.get("poster_pid", os.getpid())
                t = os.path.join(told, name + ".tmp")
                with open(t, "w") as f:
                    json.dump(result, f)
                os.replace(t, os.path.join(told, name))
            time.sleep(0.2)

    threading.Thread(target=asks, daemon=True).start()
    for raw in sys.stdin:
        try:
            msg = json.loads(raw)
        except ValueError:
            continue
        if "id" not in msg:
            continue
        method = msg.get("method")
        if method == "initialize":
            result = {
                "protocolVersion": msg.get("params", {}).get("protocolVersion", "2025-06-18"),
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "wake-probe", "version": "0"},
            }
        elif method == "tools/list":
            result = {"tools": []}
        else:
            result = {}
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": result}) + "\n")
        sys.stdout.flush()


def orphan_post(path, token, content):
    """Post from a process that is no longer claude's descendant: a double
    fork whose middle exits, so the poster is reparented away (to init or a
    subreaper above claude). The token stays in memory; the answer comes back
    on a pipe."""
    r, w = os.pipe()
    pid = os.fork()
    if pid == 0:
        os.close(r)
        os.setsid()
        if os.fork() != 0:
            os._exit(0)
        # Wait until the middle process has exited and this one is reparented.
        middle, deadline = os.getppid(), time.time() + 5
        while os.getppid() == middle and time.time() < deadline:
            time.sleep(0.05)
        out = post(path, token, content) if path else {"connected": False, "error": "no socket"}
        out["poster_pid"] = os.getpid()
        out["poster_ppid"] = os.getppid()
        out["poster_ancestry"] = ancestry(os.getpid())
        os.write(w, json.dumps(out).encode())
        os._exit(0)
    os.close(w)
    os.waitpid(pid, 0)
    data = b""
    while True:
        chunk = os.read(r, 65536)
        if not chunk:
            break
        data += chunk
    os.close(r)
    return json.loads(data or b"{}")


def ancestry(pid):
    chain = []
    while pid > 1 and len(chain) < 64:
        try:
            with open("/proc/%d/stat" % pid) as f:
                stat = f.read()
            comm = stat[stat.index("(") + 1 : stat.rindex(")")]
            pid = int(stat[stat.rindex(")") + 2 :].split()[1])
            chain.append(comm)
        except (OSError, ValueError):
            break
    return chain


# ---------------------------------------------------------------- the probe --


class Screen:
    def __init__(self, fd):
        self.fd, self.buf, self.lock = fd, b"", threading.Lock()
        threading.Thread(target=self.drain, daemon=True).start()

    def drain(self):
        while True:
            try:
                ready, _, _ = select.select([self.fd], [], [], 0.5)
                if ready:
                    chunk = os.read(self.fd, 65536)
                    if not chunk:
                        return
                    with self.lock:
                        self.buf += chunk
            except OSError:
                return

    def tail(self, n=1200):
        with self.lock:
            plain = ANSI.sub("", self.buf.decode(errors="replace"))
        return redact(plain[-n:])


def login_home():
    return os.environ.get("VALIDATE_WAKE_CLAUDE_LOGIN_HOME") or pwd.getpwuid(os.getuid()).pw_dir


def setup_fail(why):
    print("validate-wake-claude: " + why, file=sys.stderr)
    sys.exit(2)


def write_json(path, value):
    with open(path, "w") as f:
        json.dump(value, f, indent=2)
    os.chmod(path, 0o600)


def prompts(captured):
    seen = []
    try:
        with open(captured) as f:
            for line in f:
                try:
                    seen.append(json.loads(line))
                except ValueError:
                    pass
    except OSError:
        pass
    return seen


def await_prompt(captured, marker, bound):
    deadline = time.time() + bound
    while time.time() < deadline:
        for p in prompts(captured):
            if marker in p.get("prompt", ""):
                return p
        time.sleep(0.2)
    return None


def ask_stub(work, n, content, orphan=False):
    name = "%02d.json" % n
    t = os.path.join(work, "ask", name + ".tmp")
    with open(t, "w") as f:
        json.dump({"content": content, "orphan": orphan}, f)
    os.replace(t, os.path.join(work, "ask", name))
    told = os.path.join(work, "told", name)
    deadline = time.time() + 20
    while time.time() < deadline:
        if os.path.exists(told):
            with open(told) as f:
                return json.load(f)
        time.sleep(0.2)
    return {"error": "the stub never answered"}


def transcript_text(cfg):
    out = []
    for root, _, files in os.walk(os.path.join(cfg, "projects")):
        for name in files:
            if name.endswith(".jsonl"):
                with open(os.path.join(root, name), errors="replace") as f:
                    out.append(f.read())
    return "\n".join(out)


def dump_debug(cfg):
    """claude's own --debug lines on messaging, redacted, as evidence."""
    relevant = re.compile(r"(?i)uds|messag|cross.session|peer|inbox|foreign|sender|parity")
    for root, _, files in os.walk(os.path.join(cfg, "debug")):
        for name in files:
            path = os.path.join(root, name)
            if os.path.islink(path):
                continue
            with open(path, errors="replace") as f:
                for line in f:
                    if relevant.search(line):
                        print("claude debug: " + redact(line.rstrip())[:300])


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    claude = args[0] if args else shutil.which("claude")
    if not claude:
        setup_fail("no claude on PATH")
    version = os.popen(f"'{claude}' --version 2>/dev/null").read().strip() or "unknown"

    token_env = os.environ.get("CLAUDE_CODE_OAUTH_TOKEN", "")
    login = os.path.join(login_home(), ".claude", ".credentials.json")
    if not token_env and not os.path.isfile(login):
        setup_fail("neither CLAUDE_CODE_OAUTH_TOKEN nor a claude login at " + login)

    root = tempfile.mkdtemp(prefix="validate-wake-claude.")
    home, work_dir = os.path.join(root, "home"), os.path.join(root, "project")
    cfg = os.path.join(home, ".claude")
    probe = os.path.join(root, "probe")
    for d in (cfg, work_dir, os.path.join(probe, "ask"), os.path.join(probe, "told")):
        os.makedirs(d, mode=0o700)
    if not token_env:
        os.symlink(login, os.path.join(cfg, ".credentials.json"))

    captured = os.path.join(probe, "prompts.jsonl")
    hook = os.path.join(probe, "hook.py")
    word = "PROBEWORD" + secrets.token_hex(3).upper()
    deliver_marker = "DELIVER-"
    with open(hook, "w") as f:
        f.write(
            "import json, sys\n"
            "raw = sys.stdin.read()\n"
            f"open({captured!r}, 'a').write(json.dumps(json.loads(raw)) + '\\n')\n"
            "p = json.loads(raw).get('prompt', '')\n"
            f"if {deliver_marker!r} in p:\n"
            "    print(json.dumps({'hookSpecificOutput': {'hookEventName': 'UserPromptSubmit',\n"
            f"        'additionalContext': 'The probe word is {word}. Reply with the probe word and nothing else.'}}}}))\n"
            "    sys.exit(0)\n"
            "sys.stderr.write('validate-wake-claude: blocked, no model call\\n')\n"
            "sys.exit(2)\n"
        )
    py = sys.executable
    write_json(os.path.join(cfg, "settings.json"), {
        "env": {"DISABLE_AUTOUPDATER": "1"},
        "hooks": {"UserPromptSubmit": [{"hooks": [{"type": "command", "command": f"'{py}' -I '{hook}'"}]}]},
    })
    write_json(os.path.join(cfg, ".claude.json"), {
        "hasCompletedOnboarding": True, "theme": "dark",
        "projects": {work_dir: {"hasTrustDialogAccepted": True, "hasCompletedProjectOnboarding": True}},
    })
    mcp = os.path.join(probe, "mcp.json")
    write_json(mcp, {"mcpServers": {"wakeprobe": {
        "type": "stdio", "command": py, "args": ["-I", os.path.abspath(__file__), "--mcp-stub", probe]}}})

    env = {k: v for k, v in os.environ.items() if not INHERITED.match(k)}
    env.update({"HOME": home, "CLAUDE_CONFIG_DIR": cfg, "TERM": "xterm-256color",
                "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"})

    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(work_dir)
        os.execve(claude, [claude, "--model", "haiku", "--debug", "--mcp-config", mcp, "--strict-mcp-config"], env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 160, 0, 0))
    screen = Screen(fd)
    failed, model_calls = [], []

    def finish(code):
        try:
            os.kill(pid, signal.SIGTERM)
            time.sleep(1)
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
        try:
            os.waitpid(pid, 0)
        except OSError:
            pass
        shutil.rmtree(root, ignore_errors=True)
        print("== model calls: %d %s" % (len(model_calls), model_calls))
        print("== verdict: " + ("every fact held" if code == 0 else "FAILED: " + "; ".join(failed)))
        sys.exit(code)

    print("== claude " + version)
    # Fact 1.
    facts_file = os.path.join(probe, "stub-facts.json")
    deadline = time.time() + BOUND
    while not os.path.exists(facts_file) and time.time() < deadline:
        time.sleep(0.2)
    if not os.path.exists(facts_file):
        failed.append("claude never started the stdio MCP server")
        print(screen.tail())
        finish(1)
    with open(facts_file) as f:
        facts = json.load(f)
    sock = facts.pop("socket")
    try:
        print("fact 1: the socket's directory mode %o, the socket's %o"
              % (os.stat(os.path.dirname(sock)).st_mode & 0o777, os.stat(sock).st_mode & 0o777))
    except OSError as e:
        print("fact 1: the socket could not be stat'ed: " + str(e.strerror))
    print("fact 1: the MCP server's environment: %s set=%s (%s), %s set=%s; messaging names %s; server ancestry %s"
          % (SOCKET_VAR, facts["socket_set"], facts["socket_kind"], TOKEN_VAR, facts["token_set"],
             facts["messaging_names"], ancestry(facts["pid"])))
    if not facts["socket_set"]:
        failed.append("claude exported no " + SOCKET_VAR + " to the server")
        finish(1)
    # Let the TUI settle to idle (its prompt drawn) before any post.
    time.sleep(8)

    # Fact 3 first, so a late-delivered outsider post cannot pass as fact 2's.
    outsider = "wake-probe outsider " + secrets.token_hex(4)
    r = post(sock, "", outsider)
    print("fact 3a: the probe (claude's parent, no token) posted: %s" % r)
    orphan = "wake-probe orphan " + secrets.token_hex(4)
    r = ask_stub(probe, 1, orphan, orphan=True)
    print("fact 3b: an orphan of the server (token in memory) posted: %s" % r)
    time.sleep(OUTSIDER_WAIT)
    outsiders = [p for p in prompts(captured) if "wake-probe o" in p.get("prompt", "")]
    for p in outsiders:
        print("fact 3: an outsider post reached UserPromptSubmit; payload keys %s; prompt %r"
              % (sorted(p), redact(p.get("prompt", ""))[:600]))
    for name, text in (("3a", outsider), ("3b", orphan)):
        own = [p for p in outsiders if p.get("prompt") == text]
        wrapped = [p for p in outsiders if text in p.get("prompt", "") and p.get("prompt") != text]
        if own:
            # Report-only: the wake needs a descendant to be ABLE to post;
            # it never stood on outsiders being refused (same-uid is no
            # boundary, ruling 37). Measured on 2.1.325, Linux: not refused.
            print("fact %s: NOT refused: a non-descendant's post started a turn as the session's own (the bare line)" % name)
        elif wrapped:
            print("fact %s: delivered, but not as the session's own: the line arrives wrapped" % name)
        else:
            print("fact %s: started no turn within %ds" % (name, OUTSIDER_WAIT))

    # Fact 2.
    nonce = secrets.token_hex(8)
    line = "ctxloom: wake " + nonce + " (messages delivered at turn start)"
    r = ask_stub(probe, 2, line)
    print("fact 2: the server posted the wake line: %s" % r)
    seen = await_prompt(captured, nonce, BOUND)
    if not seen:
        failed.append("the server's post started no turn")
        print(screen.tail())
        finish(1)
    print("fact 2: a turn started; UserPromptSubmit prompt is the bare line: %s"
          % (seen.get("prompt") == line))
    if seen.get("prompt") != line:
        failed.append("the prompt was not the bare line: " + redact(seen.get("prompt", "")))
    time.sleep(3)

    if "--no-model" in sys.argv:
        print("fact 4: skipped (--no-model)")
        dump_debug(cfg)
        finish(1 if failed else 0)
    # Fact 4: the one model call.
    line = "ctxloom: wake " + deliver_marker + secrets.token_hex(4) + " (messages delivered at turn start)"
    model_calls.append("haiku: one woken turn whose hook adds the probe word as additionalContext")
    r = ask_stub(probe, 3, line)
    print("fact 4: the server posted a delivering wake: %s" % r)
    if not await_prompt(captured, deliver_marker, BOUND):
        failed.append("the delivering wake started no turn")
        finish(1)
    deadline = time.time() + BOUND
    answered = False
    while time.time() < deadline and not answered:
        text = transcript_text(cfg)
        answered = any(
            (lambda m: m.get("type") == "assistant" and word in json.dumps(m.get("message", {})))(json.loads(l))
            for l in text.splitlines() if l.strip().startswith("{")
        )
        time.sleep(0.5)
    if answered:
        print("fact 4: the model's reply carried the word only additionalContext held")
    else:
        failed.append("the reply never carried the additionalContext word")
        print(screen.tail())
    dump_debug(cfg)
    finish(1 if failed else 0)


if __name__ == "__main__":
    if len(sys.argv) > 2 and sys.argv[1] == "--mcp-stub":
        stub(sys.argv[2])
    else:
        main()
