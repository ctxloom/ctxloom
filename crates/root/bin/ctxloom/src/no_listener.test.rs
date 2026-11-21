//! Slice 4 U7 (owner ruling s4-Q3, OD-13): no `ctxloom run` serves a
//! listener of its own. A one-shot (hosted by the user's daemon) and a
//! terminal session (the daemon's prepared launch, started by its frontend)
//! put none of `CTXLOOM_ENDPOINT`, `CTXLOOM_BEARER` and `CTXLOOM_TLS_CERT` in
//! the engine's environment, and no ctxloom process listens on any TCP port
//! but the daemon's own one (its discovery endpoint). Hooks reach the daemon
//! with the user bearer (U2).
//!
//! The stub engine looks while it runs (so nothing waits for a listener to
//! come or go): it records its environment and every listening TCP socket
//! held by a process of the binary under test whose environment names this
//! test's home (the run's frontend or client, and the daemon), read from
//! `/proc` (Linux; elsewhere the test is skipped, said). The daemon is still
//! serving after the run, and says its own pid and endpoint.

use std::collections::BTreeSet;
use std::path::Path;

use super::library::Project;
use crate::tests::files;

/// The stub engine: a `claude -p` turn (it answers as claude's stream-json
/// does) or the terminal session (it answers nothing), which records what
/// it was given and who listens, then exits 0.
const STUB: &str = r#"here="$(dirname "$0")"
binary="$(cat "$here/binary")"
env > "$here/engine-env"
: > "$here/listening"
for proc in /proc/[0-9]*; do
  [ "$(readlink "$proc/exe" 2>/dev/null)" = "$binary" ] || continue
  tr '\0' '\n' < "$proc/environ" 2>/dev/null | grep -qF "=$CTXLOOM_HOME" || continue
  pid="${proc#/proc/}"
  for fd in "$proc"/fd/*; do
    link="$(readlink "$fd" 2>/dev/null)" || continue
    case "$link" in
      socket:\[*\]) inode="${link#socket:[}"; inode="${inode%]}" ;;
      *) continue ;;
    esac
    awk -v i="$inode" -v pid="$pid" 'FNR > 1 && $4 == "0A" && $10 == i { n = split($2, a, ":"); print pid, a[n] }' /proc/net/tcp /proc/net/tcp6 >> "$here/listening"
  done
done
case " $* " in
  *" -p "*)
    cat > /dev/null
    echo '{"type":"system","subtype":"init","session_id":"0b0e7d0e-5d1c-4f3e-9d0a-7c1a2b3c4d5e"}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"NO-LISTENER-REPLY","session_id":"0b0e7d0e-5d1c-4f3e-9d0a-7c1a2b3c4d5e"}'
    ;;
esac
"#;

/// The variables the per-run listener told an engine, which no host run
/// tells any more (M10b's container arrival is the one that will, OD-13).
const LISTENER_VARIABLES: [&str; 3] = ["CTXLOOM_ENDPOINT", "CTXLOOM_BEARER", "CTXLOOM_TLS_CERT"];

/// A project with a `dev` profile and the stub as `claude` in `bin` (which
/// leads the run's `PATH`), told the canonical path of the binary under
/// test; whether this host can run it.
fn project() -> Option<(Project, std::path::PathBuf, String)> {
    if !files::runs_scripts() {
        return None;
    }
    if !files::exists("/proc/self/fd") {
        eprintln!("SKIP no_listener: no /proc to read listening sockets from");
        return None;
    }
    let project = Project::initialized();
    project.bundle("demo");
    project.profile("dev", "demo");
    let bin = project.home().join("bin");
    files::put_program(bin.join("claude"), STUB);
    let binary = files::canonical(crate::tests::binary());
    files::put(bin.join("binary"), binary.display().to_string().as_bytes());
    let path = match crate::tests::host_var("PATH") {
        Some(host) => format!("{}:{}", bin.display(), host.to_string_lossy()),
        None => bin.display().to_string(),
    };
    Some((project, bin, path))
}

/// What the stub recorded of the run: its environment, the daemon's pid and
/// port, and each `(pid, port)` a ctxloom process listened on.
struct Seen {
    env: Vec<(String, String)>,
    daemon_pid: u64,
    daemon_port: u16,
    listening: BTreeSet<(u64, u16)>,
}

fn seen(project: &Project, bin: &Path) -> Seen {
    let env = files::read(bin.join("engine-env")).expect("the engine ran");
    let env = env
        .lines()
        .filter_map(|l| l.split_once('='))
        .map(|(n, v)| (n.to_string(), v.to_string()))
        .collect();
    let status = project
        .run(&["daemon", "status", "--format", "json"])
        .json();
    let daemon_pid = status["pid"]
        .as_u64()
        .unwrap_or_else(|| panic!("the daemon's pid: {status}"));
    let endpoint = status["endpoint"]
        .as_str()
        .unwrap_or_else(|| panic!("the daemon's endpoint: {status}"));
    let daemon_port = endpoint
        .rsplit_once(':')
        .and_then(|(_, port)| port.parse().ok())
        .unwrap_or_else(|| panic!("a port in {endpoint}"));
    let listening = files::read(bin.join("listening"))
        .expect("the stub looked")
        .lines()
        .filter_map(|l| l.split_once(' '))
        .map(|(pid, port)| {
            (
                pid.parse().expect("a pid"),
                u16::from_str_radix(port, 16).expect("a hex port"),
            )
        })
        .collect();
    Seen {
        env,
        daemon_pid,
        daemon_port,
        listening,
    }
}

/// The run gave its engine none of the listener's variables (but its
/// session and run, which a hook's facts are addressed by), and the only
/// TCP port a ctxloom process listened on was the daemon's own.
fn no_listener(seen: &Seen) {
    let names: Vec<&str> = seen.env.iter().map(|(n, _)| n.as_str()).collect();
    for name in LISTENER_VARIABLES {
        assert!(
            !names.contains(&name),
            "the engine was given {name}: {names:?}"
        );
    }
    for name in ["CTXLOOM_SESSION", "CTXLOOM_RUN"] {
        assert!(names.contains(&name), "the engine lacked {name}: {names:?}");
    }
    assert_eq!(
        seen.listening,
        BTreeSet::from([(seen.daemon_pid, seen.daemon_port)]),
        "a ctxloom process listened beside the daemon's endpoint"
    );
}

/// Stop the test home's daemon (it would leave on its idle limit anyway).
fn stop_daemon(project: &Project) {
    let _ = project.run(&["daemon", "stop"]);
}

#[test]
fn test_a_one_shot_serves_no_listener_and_tells_its_engine_none() {
    let Some((project, bin, path)) = project() else {
        return;
    };
    let ran = project.run_with(
        &[
            ("PATH", &path),
            ("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture-not-real"), // gitleaks:allow
        ],
        &[
            "run",
            "--profile",
            "dev",
            "--one-shot",
            "hello",
            "--format",
            "json",
        ],
    );
    let seen = files::exists(bin.join("engine-env")).then(|| seen(&project, &bin));
    stop_daemon(&project);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(ran.stdout.contains("NO-LISTENER-REPLY"), "{}", ran.said());
    no_listener(&seen.expect("the engine ran"));
}

#[test]
fn test_a_terminal_session_serves_no_listener_and_tells_its_engine_none() {
    let Some((project, bin, path)) = project() else {
        return;
    };
    let ran = project.run_with(
        &[("PATH", &path), ("CTXLOOM_TEST_TERMINAL", "1")],
        &["run", "--profile", "dev"],
    );
    let seen = files::exists(bin.join("engine-env")).then(|| seen(&project, &bin));
    stop_daemon(&project);
    assert_eq!(ran.code, 0, "{}", ran.said());
    no_listener(&seen.expect("the engine ran"));
}
