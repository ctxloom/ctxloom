//! The launch to the wire and back: every field the wire has, and nothing
//! it has not.

use std::collections::BTreeMap;
use std::time::Duration;

use daemon_proto::v1;
use run_ports::{Ignored, Launch, LaunchAt, LaunchKind, LeftOut, Stdio, StopPolicy, TurnCommand};

use crate::launch::{from_wire, to_wire};

fn launch(kind: LaunchKind, stdio: Stdio) -> Launch {
    let mut command = TurnCommand::new("/usr/bin/claude");
    command.args = vec!["--settings".to_string(), "/h/hooks.json".to_string()];
    command.env = BTreeMap::from([
        ("PATH".to_string(), "/usr/bin".to_string()),
        ("CLAUDE_CONFIG_DIR".to_string(), "/h".to_string()),
    ]);
    command.cwd = Some("/work".to_string());
    let mut launch = Launch::new(kind, "claude-code", stdio, command);
    launch.at = LaunchAt {
        project_id: "calm-otter".to_string(),
        session: "5f0c7a52-1111-5111-8111-000000000001".to_string(),
        harp: "amber-swift-owl".to_string(),
        run: "run-1".to_string(),
        incarnation: 3,
    };
    launch.session_home = "/work/.ctxloom/sessions/amber-swift-owl/home".to_string();
    launch.session_lock = "/work/.ctxloom/sessions/amber-swift-owl/session.lock".to_string();
    launch
}

#[test]
fn every_kind_and_stdio_goes_to_the_wire_and_comes_back_as_it_was() {
    for (kind, stdio) in [
        (LaunchKind::Terminal, Stdio::Inherit),
        (LaunchKind::Child, Stdio::StreamJson),
        (LaunchKind::Child, Stdio::Acp),
        (LaunchKind::OneShot, Stdio::Piped),
        (LaunchKind::Editor, Stdio::Inherit),
    ] {
        let sent = launch(kind, stdio);
        assert_eq!(from_wire(&to_wire(&sent)), Some(sent), "{kind:?}");
    }
}

#[test]
fn the_wire_holds_the_launch_in_its_own_words() {
    let wire = to_wire(&launch(LaunchKind::Terminal, Stdio::Inherit));
    assert_eq!(wire.kind, v1::launch::Kind::Terminal as i32);
    assert_eq!(wire.stdio, v1::launch::Stdio::Inherit as i32);
    assert_eq!(wire.engine, "claude-code");
    assert_eq!(wire.program, "/usr/bin/claude");
    assert_eq!(wire.cwd, "/work");
    let names: Vec<&str> = wire.env.iter().map(|v| v.name.as_str()).collect();
    assert_eq!(names, ["CLAUDE_CONFIG_DIR", "PATH"], "sorted by name");
    assert_eq!(wire.env[1].value, "/usr/bin");
    let at = wire.at.as_ref().expect("at");
    assert_eq!((at.run.as_str(), at.incarnation), ("run-1", 3));
    let stop = wire.stop.expect("stop");
    assert_eq!((stop.signal, stop.grace_ms), (15, 5000));
    assert!(!stop.close_stdin_first);
    let piped = to_wire(&launch(LaunchKind::OneShot, Stdio::Piped));
    assert_eq!(piped.stdio, v1::launch::Stdio::Unspecified as i32);
    assert_eq!(piped.kind, v1::launch::Kind::OneShot as i32);
    let child = to_wire(&launch(LaunchKind::Child, Stdio::Acp));
    assert_eq!(child.stdio, v1::launch::Stdio::Acp as i32);
    assert_eq!(child.kind, v1::launch::Kind::Child as i32);
    assert!(child.stop.expect("stop").close_stdin_first);
    let editor = to_wire(&launch(LaunchKind::Editor, Stdio::StreamJson));
    assert_eq!(editor.kind, v1::launch::Kind::Editor as i32);
    assert_eq!(editor.stdio, v1::launch::Stdio::StreamJson as i32);
}

#[test]
fn what_the_launch_said_and_its_input_never_cross_the_wire() {
    let mut sent = launch(LaunchKind::OneShot, Stdio::Piped);
    sent.stdin = b"the prompt".to_vec();
    sent.ignored = vec![Ignored::Owned {
        permit: "CTXLOOM_PERMIT_PATH".to_string(),
    }];
    sent.left_out = vec![LeftOut {
        engine: "claude-code".to_string(),
        path: "/home/u/.claude/settings.json".to_string(),
    }];
    let back = from_wire(&to_wire(&sent)).unwrap();
    assert!(back.stdin.is_empty());
    assert!(back.ignored.is_empty());
    assert!(back.left_out.is_empty());
    assert_eq!(back.env, sent.env);
}

#[test]
fn a_launch_with_no_directory_or_stop_reads_as_none_and_the_kinds_own() {
    let mut sent = launch(LaunchKind::Terminal, Stdio::Inherit);
    sent.cwd = None;
    let mut wire = to_wire(&sent);
    assert_eq!(wire.cwd, "");
    wire.stop = None;
    wire.at = None;
    let back = from_wire(&wire).unwrap();
    assert_eq!(back.cwd, None);
    assert_eq!(back.stop, StopPolicy::of(LaunchKind::Terminal));
    assert_eq!(back.at, LaunchAt::default());
    wire.stop = Some(v1::StopPolicy {
        signal: 2,
        grace_ms: 250,
        close_stdin_first: true,
    });
    let stop = from_wire(&wire).unwrap().stop;
    assert_eq!(stop.signal, 2);
    assert_eq!(stop.grace, Duration::from_millis(250));
    assert!(stop.close_stdin_first);
}

#[test]
fn a_launch_of_no_kind_it_knows_is_none() {
    let mut wire = to_wire(&launch(LaunchKind::Terminal, Stdio::Inherit));
    wire.kind = v1::launch::Kind::Unspecified as i32;
    assert_eq!(from_wire(&wire), None);
    wire.kind = 99;
    assert_eq!(from_wire(&wire), None);
}
