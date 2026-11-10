//! Acceptance: the hidden `hook` commands, through the real binary. Ported
//! from Go's tests/acceptance/features/cli/session_hooks.feature
//! (release/0.7): each scenario is named in a comment.
//!
//! These cover the machine contract: a hook warns and exits 0 on what it can
//! foresee, and stdout is the engine's input, so the stdout assertions parse
//! stdout ALONE, never the combined stream.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use super::{binary, Env, Ran};
use crate::tests::files;

impl Env {
    /// Run `ctxloom args` with `stdin` on its standard input and `vars` in its
    /// environment (this home, and nothing else of the caller's).
    pub(crate) fn run_hook(&self, args: &[&str], stdin: &str, vars: &[(&str, &str)]) -> Ran {
        let mut command = Command::new(binary());
        command
            .args(args)
            .env_clear()
            .env("HOME", self.home())
            .env("USERPROFILE", self.home())
            .env("TMPDIR", crate::tests::temp_root())
            .current_dir(self.home())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        for name in ["PATH", "SystemRoot"] {
            if let Some(value) = crate::tests::host_var(name) {
                command.env(name, value);
            }
        }
        for (name, value) in vars {
            command.env(name, value);
        }
        let mut child = command.spawn().expect("ctxloom runs");
        crate::tests::feed(child.stdin.take().expect("stdin"), stdin);
        Ran::of(child.wait_with_output().expect("ctxloom finishes"))
    }
}

impl Env {
    /// Run `ctxloom args` as the interactive engine's hook process runs it:
    /// no `HOME` (nor `USERPROFILE`), `vars` and the engine's `PATH` only.
    pub(crate) fn run_hook_without_home(
        &self,
        args: &[&str],
        stdin: &str,
        vars: &[(&str, &str)],
    ) -> Ran {
        let mut command = Command::new(binary());
        command
            .args(args)
            .env_clear()
            .env("TMPDIR", crate::tests::temp_root())
            .current_dir(self.home())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        for name in ["PATH", "SystemRoot"] {
            if let Some(value) = crate::tests::host_var(name) {
                command.env(name, value);
            }
        }
        for (name, value) in vars {
            command.env(name, value);
        }
        let mut child = command.spawn().expect("ctxloom runs");
        crate::tests::feed(child.stdin.take().expect("stdin"), stdin);
        Ran::of(child.wait_with_output().expect("ctxloom finishes"))
    }
}

/// Every path under `dir`, relative to it, sorted.
fn tree(dir: &Path) -> Vec<PathBuf> {
    fn walk(dir: &Path, root: &Path, found: &mut Vec<PathBuf>) {
        for path in files::children(dir) {
            found.push(
                path.strip_prefix(root)
                    .expect("under the root")
                    .to_path_buf(),
            );
            if files::is_dir(&path) {
                walk(&path, root, found);
            }
        }
    }
    let mut found = Vec::new();
    walk(dir, dir, &mut found);
    found.sort();
    found
}

const HUD_PAYLOAD: &str =
    r#"{"model":{"display_name":"Opus 5"},"context_window":{"used_percentage":42}}"#;

// Go's line, on the wire: no trailing newline (fmt.Print), the glyph and the
// separator.
#[test]
fn test_hud_prints_gos_line_without_a_newline() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        HUD_PAYLOAD,
        &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
    );
    assert!(
        ran.stdout
            .starts_with("\u{1b}[36mOpus 5\u{1b}[0m \u{2502} "),
        "{:?}",
        ran.stdout
    );
    assert!(
        ran.stdout.contains("\u{2301} brisk-copper-moth"),
        "{:?}",
        ran.stdout
    );
    assert!(!ran.stdout.ends_with('\n'), "{:?}", ran.stdout);
}

// D5 (2026-11-02): the HUD hook records no context sample. Go wrote
// context-metrics.jsonl under the session's directory from here.
#[test]
fn test_hud_records_nothing() {
    let env = Env::new();
    let before = tree(env.home());
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        HUD_PAYLOAD,
        &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(tree(env.home()), before, "the home is as it was");
}

// The warning rides the diagnostic channel in the format the process chose:
// an explicit json format makes it an envelope, still on stderr.
#[test]
fn test_session_start_warning_is_an_envelope_under_json() {
    let env = Env::new();
    let ran = env.run_hook(
        &[
            "--format",
            "json",
            "hook",
            "claude-code",
            "execute",
            "SessionStart",
        ],
        "{}",
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout, "");
    assert_eq!(ran.err_json()["code"], "hook-cli-no-usable-harp");
}

// session_hooks.feature: "The retired turn-changed callback is not a command".
#[test]
fn test_the_retired_turn_changed_hook_is_not_a_command() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "turn-changed"],
        r#"{"hook_event_name":"Stop"}"#,
        &[],
    );
    assert_eq!(ran.code, 2, "{}", ran.said());
}

// Go: the hook namespace is hidden ("Machine callback ... not for direct
// use"), but answers -h.
#[test]
fn test_hook_is_hidden_from_the_root_help_and_answers_help() {
    let env = Env::new();
    let root = env.run(&["-h"]);
    assert_eq!(root.code, 0, "{}", root.said());
    assert!(
        !root
            .stdout
            .lines()
            .any(|l| l.trim_start().starts_with("hook")),
        "{}",
        root.stdout
    );
    let hook = env.run(&["hook", "-h"]);
    assert_eq!(hook.code, 0, "{}", hook.said());
}

// ---------------------------------------------------------------------------
// `hook <engine> execute <hook-name>`, the one form
// (owner, 2026-11-08). The engine is validated by the registry, the hook name
// is the engine's native event; the retired `session-bind` and `hud` are
// `hook claude-code execute SessionStart` and `... statusline`.

#[test]
fn test_execute_statusline_renders_the_engines_half_and_ctxloom_s_own() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        HUD_PAYLOAD,
        &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    for want in ["Opus 5", "42%", "brisk-copper-moth"] {
        assert!(ran.stdout.contains(want), "{want:?} in {}", ran.said());
    }
    assert_eq!(ran.stderr, "", "a diagnostic would corrupt the bar");
    assert!(!ran.stdout.ends_with('\n'));
}

#[test]
fn test_execute_statusline_on_garbage_is_the_sentinel_and_exit_0() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        "not json",
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout, "ctxloom");
}

// The typed refusal lists the registry's names; nothing is guessed.
#[test]
fn test_execute_under_an_unknown_engine_is_refused_listing_the_known_ones() {
    let env = Env::new();
    let ran = env.run_hook(
        &[
            "--format",
            "json",
            "hook",
            "no-such-engine",
            "execute",
            "Stop",
        ],
        "{}",
        &[],
    );
    assert_eq!(ran.code, 1, "{}", ran.said());
    assert_eq!(ran.stdout, "");
    let err = ran.err_json();
    assert_eq!(err["code"], "setup-application-unknown-hook-engine");
    assert_eq!(err["args"]["engine"], "no-such-engine");
    assert!(
        err["args"]["known"]
            .as_str()
            .unwrap()
            .contains("claude-code"),
        "{err}"
    );
}

/// The warnings a hook said on stderr: one JSON document each.
fn warnings(stderr: &str) -> Vec<serde_json::Value> {
    serde_json::Deserializer::from_str(stderr)
        .into_iter::<serde_json::Value>()
        .map(|w| w.expect("stderr holds JSON warnings only"))
        .collect()
}

/// `stderr` is exactly `expected` not-recorded warnings.
fn assert_not_recorded(stderr: &str, expected: usize, what: &str) {
    let said = warnings(stderr);
    assert_eq!(said.len(), expected, "{what}: {stderr}");
    for warning in &said {
        assert_eq!(
            warning["code"], "hook-cli-not-recorded",
            "{what}: {warning}"
        );
    }
}

// The events that carry the primary agent's data are answered as silence, and
// each observation they hold that cannot be delivered (no listener yet) is
// dropped with a stderr note: owner ruling C. An event that observes nothing
// says nothing.
#[test]
fn test_execute_of_the_data_events_answers_silence_and_notes_what_it_did_not_record() {
    let env = Env::new();
    let fixtures = [
        (
            "Stop",
            r#"{"session_id":"s","prompt_id":"p","hook_event_name":"Stop","last_assistant_message":"done"}"#,
            1,
        ),
        (
            "MessageDisplay",
            r#"{"session_id":"s","prompt_id":"p","hook_event_name":"MessageDisplay","turn_id":"t","message_id":"m","index":0,"final":true,"delta":"done"}"#,
            1,
        ),
        (
            "PostToolBatch",
            r#"{"session_id":"s","prompt_id":"p","hook_event_name":"PostToolBatch","tool_calls":[]}"#,
            0,
        ),
    ];
    for (native, payload, observations) in fixtures {
        let ran = env.run_hook(
            &["hook", "claude-code", "execute", native],
            payload,
            &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
        );
        assert_eq!(ran.code, 0, "{native}: {}", ran.said());
        assert_eq!(ran.stdout.trim(), "{}", "{native}");
        assert_not_recorded(&ran.stderr, observations, native);
    }
}

const RECORDED_HARP: &str = "brisk-copper-moth";
const STOP: &str = r#"{"session_id":"s","prompt_id":"p","hook_event_name":"Stop","last_assistant_message":"done"}"#;
/// A well formed bearer: `<run>.<64 hex>`.
const A_BEARER: &str = "0a1b2c3d-0000-4000-8000-000000000001.\
    abababababababababababababababababababababababababababababababab"; // gitleaks:allow

impl Env {
    /// The session directory a harp's hook finds its session in.
    fn with_session_directory(&self, harp: &str) {
        files::put(
            self.home()
                .join(".ctxloom/sessions")
                .join(harp)
                .join("metadata.json"),
            format!("{{\"v\": 1, \"id\": \"0a1b2c3d-0000-4000-8000-0000000000aa\", \"harp\": \"{harp}\"}}\n"),
        );
    }
}

// The hook's delivery with no endpoint in its environment is as it was before
// the listener: the observation is not recorded, said once, and the hook
// answers and exits 0.
#[test]
fn test_a_hook_with_no_endpoint_says_the_listener_is_not_running() {
    let env = Env::new();
    env.with_session_directory(RECORDED_HARP);
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "Stop"],
        STOP,
        &[("CTXLOOM_SESSION_HARP", RECORDED_HARP)],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout.trim(), "{}");
    assert_not_recorded(&ran.stderr, 1, "no endpoint");
    assert!(
        ran.stderr.contains("listener is not running"),
        "{}",
        ran.stderr
    );
}

// Review finding 6 (ruling 87): a hook finds its session from the run, by the
// id its environment names (`CTXLOOM_SESSION`), with no session directory and
// no metadata.json to read: it gets as far as the delivery, which fails open
// for want of a listener here.
#[test]
fn test_a_hook_finds_its_session_from_the_run_with_no_session_directory() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "Stop"],
        STOP,
        &[
            ("CTXLOOM_SESSION_HARP", RECORDED_HARP),
            ("CTXLOOM_SESSION", "0a1b2c3d-0000-4000-8000-0000000000aa"),
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout.trim(), "{}");
    assert!(
        !ran.stderr.contains("no session directory"),
        "the session is the run's, not a directory's: {}",
        ran.stderr
    );
    assert!(
        ran.stderr.contains("listener is not running"),
        "{}",
        ran.stderr
    );
}

/// A loopback URL nobody listens on: a port that was bound, then released.
fn closed_endpoint() -> String {
    let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = probe.local_addr().unwrap().port();
    drop(probe);
    format!("https://127.0.0.1:{port}")
}

// A malformed bearer is no credential: the same fail-open, and the bearer text
// is never echoed.
#[test]
fn test_a_hook_with_a_malformed_bearer_fails_open_without_echoing_it() {
    let env = Env::new();
    env.with_session_directory(RECORDED_HARP);
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "Stop"],
        STOP,
        &[
            ("CTXLOOM_SESSION_HARP", RECORDED_HARP),
            ("CTXLOOM_ENDPOINT", &closed_endpoint()),
            ("CTXLOOM_BEARER", "not-a-bearer-zzz"),
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_not_recorded(&ran.stderr, 1, "malformed bearer");
    assert!(!ran.stderr.contains("not-a-bearer-zzz"), "{}", ran.stderr);
}

// An endpoint nobody listens on: the delivery is unavailable, said once per
// observation, and the hook still answers and exits 0.
#[test]
fn test_a_hook_whose_endpoint_is_dead_says_it_could_not_reach_the_listener() {
    let env = Env::new();
    env.with_session_directory(RECORDED_HARP);
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "Stop"],
        STOP,
        &[
            ("CTXLOOM_SESSION_HARP", RECORDED_HARP),
            ("CTXLOOM_ENDPOINT", &closed_endpoint()),
            ("CTXLOOM_BEARER", A_BEARER),
            ("CTXLOOM_TLS_CERT", A_CERT),
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout.trim(), "{}");
    assert_not_recorded(&ran.stderr, 1, "dead endpoint");
    assert!(
        ran.stderr.contains("cannot reach the listener"),
        "{}",
        ran.stderr
    );
    assert!(!ran.stderr.contains(A_BEARER), "{}", ran.stderr);
}

/// A certificate in the form the run sets (base64 of a DER SEQUENCE); a hook
/// that reaches no handshake needs no real one.
const A_CERT: &str = "MAMBAgM=";

// No fallback: a listener named with no usable certificate in the environment
// is refused by type, the hook still answers and exits 0, and the bearer is
// never echoed.
#[test]
fn test_a_hook_without_a_usable_certificate_is_refused_and_says_so() {
    for (cert, said) in [
        (None, "CTXLOOM_TLS_CERT is not set"),
        (Some("!!!!"), "CTXLOOM_TLS_CERT is not a certificate"),
    ] {
        let env = Env::new();
        env.with_session_directory(RECORDED_HARP);
        let endpoint = closed_endpoint();
        let mut vars = vec![
            ("CTXLOOM_SESSION_HARP", RECORDED_HARP),
            ("CTXLOOM_ENDPOINT", endpoint.as_str()),
            ("CTXLOOM_BEARER", A_BEARER),
        ];
        if let Some(cert) = cert {
            vars.push(("CTXLOOM_TLS_CERT", cert));
        }
        let ran = env.run_hook(&["hook", "claude-code", "execute", "Stop"], STOP, &vars);
        assert_eq!(ran.code, 0, "{}", ran.said());
        assert_eq!(ran.stdout.trim(), "{}");
        assert_not_recorded(&ran.stderr, 1, "no certificate");
        assert!(ran.stderr.contains(said), "{}", ran.stderr);
        assert!(!ran.stderr.contains(A_BEARER), "{}", ran.stderr);
    }
}

// No downgrade: an http endpoint is never dialed.
#[test]
fn test_a_hook_with_a_plaintext_endpoint_is_refused_and_says_so() {
    let env = Env::new();
    env.with_session_directory(RECORDED_HARP);
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "Stop"],
        STOP,
        &[
            ("CTXLOOM_SESSION_HARP", RECORDED_HARP),
            ("CTXLOOM_ENDPOINT", "http://127.0.0.1:9"),
            ("CTXLOOM_BEARER", A_BEARER),
            ("CTXLOOM_TLS_CERT", A_CERT),
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_not_recorded(&ran.stderr, 1, "plaintext");
    assert!(ran.stderr.contains("plaintext"), "{}", ran.stderr);
    assert!(!ran.stderr.contains(A_BEARER), "{}", ran.stderr);
}

// A hook with no harp has no session to address: it attempts no observation
// and warns of none (not a warning with an empty name).
#[test]
fn test_a_hook_with_no_harp_attempts_no_recording() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "Stop"],
        r#"{"session_id":"s","prompt_id":"p","hook_event_name":"Stop"}"#,
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout.trim(), "{}");
    assert_eq!(ran.stderr, "", "{}", ran.said());
}

// session_hooks.feature: "With no active harp the hook stays silent on stdout
// and warns instead", now as SessionStart.
#[test]
fn test_execute_session_start_with_no_harp_is_silent_on_stdout_and_warns() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "SessionStart"],
        r#"{"session_id":"vendor-session-42","hook_event_name":"SessionStart"}"#,
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout, "", "nothing truthful to emit");
    assert!(ran.stderr.contains("no usable harp"), "{}", ran.said());
}

// The hook name is a verb of the engine, not of ctxloom: the old verbs and the
// `--engine` flag are not commands.
#[test]
fn test_the_old_hook_verbs_are_not_commands() {
    let env = Env::new();
    for line in [
        vec!["hook", "hud"],
        vec!["hook", "session-bind", "--engine", "claude-code"],
        vec!["hook", "session-start", "--engine", "claude-code"],
        vec!["hook", "skill-mates", "--engine", "claude-code"],
    ] {
        let ran = env.run_hook(&line, "{}", &[]);
        assert_eq!(ran.code, 2, "{line:?}: {}", ran.said());
    }
}

#[test]
fn test_hook_help_names_execute() {
    let env = Env::new();
    let hook = env.run(&["hook", "-h"]);
    assert_eq!(hook.code, 0, "{}", hook.said());
    assert!(hook.stdout.contains("execute"), "{}", hook.stdout);
    assert!(!hook.stdout.contains("session-bind"), "{}", hook.stdout);
}

// Owner ruling 2026-11-08: the interactive engine's env carries no HOME, so a
// hook process finds ctxloom's home through CTXLOOM_HOME, and answers as it
// does with a HOME.
#[test]
fn test_a_hook_process_without_home_finds_the_home_through_ctxloom_home() {
    let env = Env::new();
    let home = env.home().to_string_lossy().to_string();
    let payload = r#"{"session_id":"s","prompt_id":"p","hook_event_name":"Stop","last_assistant_message":"done"}"#;
    let ran = env.run_hook_without_home(
        &["hook", "claude-code", "execute", "Stop"],
        payload,
        &[
            ("CTXLOOM_HOME", &home),
            ("CTXLOOM_SESSION_HARP", "brisk-copper-moth"),
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(
        ran.stdout.trim(),
        "{}",
        "a data event answers the empty response"
    );
    // The home was found (no no-home refusal); the one observation of a turn
    // end could not be delivered, and is said so.
    assert_not_recorded(&ran.stderr, 1, "Stop");
    let start = env.run_hook_without_home(
        &["hook", "claude-code", "execute", "SessionStart"],
        "{}",
        &[("CTXLOOM_HOME", &home)],
    );
    assert_eq!(start.code, 0, "{}", start.said());
    assert!(!start.stderr.contains("no home"), "{}", start.said());
}

// Without HOME and without the variable there is no home to find, and the
// refusal says which variables were looked for.
#[test]
fn test_a_hook_process_with_no_home_at_all_is_refused_naming_both_variables() {
    let env = Env::new();
    let ran = env.run_hook_without_home(
        &["--format", "json", "hook", "claude-code", "execute", "Stop"],
        "{}",
        &[],
    );
    assert_eq!(ran.code, 1, "{}", ran.said());
    let err = ran.err_json();
    assert_eq!(err["code"], "ctxloom-wiring-no-home");
    assert!(
        err["message"].as_str().unwrap().contains("CTXLOOM_HOME"),
        "{err}"
    );
}
