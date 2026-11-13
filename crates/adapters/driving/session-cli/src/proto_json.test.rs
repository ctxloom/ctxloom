//! Proto3 JSON, as ruling 146 has the daemon's answers printed.

use daemon_proto::v1;
use serde_json::json;
use sessions_application::Timestamp;

use super::{daemon, deletion, session, sessions};

// A field at its default is left out; an enum is its name; a Timestamp is
// RFC 3339 with Z; a uint32 a number.
#[test]
fn a_session_is_its_proto3_json() {
    let at = Timestamp {
        seconds: 1_784_309_252,
        nanos: 0,
    };
    let s = v1::Session {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        uid: "u-1".to_string(),
        state: v1::session::State::Ended as i32,
        engine: "claude-code".to_string(),
        start_time: Some(at.into()),
        kept: true,
        children: vec!["projects/calm-otter/sessions/brisk-copper-moth".to_string()],
        pending: vec![v1::PendingMessage {
            message_id: "m1".to_string(),
            urgency: "inform".to_string(),
            text: "hi".to_string(),
            sender: String::new(),
        }],
        incarnation: 2,
        title: "the auth refactor".to_string(),
        ..v1::Session::default()
    };
    assert_eq!(
        session(&s),
        json!({
            "name": "projects/calm-otter/sessions/amber-swift-owl",
            "uid": "u-1",
            "state": "ENDED",
            "engine": "claude-code",
            "start_time": "2026-07-17T17:27:32Z",
            "kept": true,
            "children": ["projects/calm-otter/sessions/brisk-copper-moth"],
            "pending": [{"message_id": "m1", "urgency": "inform", "text": "hi"}],
            "incarnation": 2,
            "title": "the auth refactor",
        })
    );
    assert_eq!(session(&v1::Session::default()), json!({}));
}

// An empty listing is the empty object; a listing's sessions are a list.
#[test]
fn a_listing_is_its_proto3_json() {
    assert_eq!(sessions(&v1::ListSessionsResponse::default()), json!({}));
    let listed = v1::ListSessionsResponse {
        sessions: vec![v1::Session {
            name: "projects/a/sessions/b".to_string(),
            ..v1::Session::default()
        }],
        next_page_token: String::new(),
    };
    assert_eq!(
        sessions(&listed),
        json!({"sessions": [{"name": "projects/a/sessions/b"}]})
    );
}

// The daemon: its 64-bit counts are decimal strings, a run's kind its name;
// it holds no bearer to print.
#[test]
fn the_daemon_is_its_proto3_json() {
    let d = v1::Daemon {
        name: "daemon".to_string(),
        pid: 4242,
        endpoint: "https://127.0.0.1:5000".to_string(),
        runs: vec![v1::HostedRun {
            harp: "brisk-copper-moth".to_string(),
            kind: v1::hosted_run::Kind::Child as i32,
            ..v1::HostedRun::default()
        }],
        idle_seconds: 0,
        idle_limit_seconds: 600,
        ..v1::Daemon::default()
    };
    assert_eq!(
        daemon(&d),
        json!({
            "name": "daemon",
            "pid": 4242,
            "endpoint": "https://127.0.0.1:5000",
            "runs": [{"harp": "brisk-copper-moth", "kind": "CHILD"}],
            "idle_limit_seconds": "600",
        })
    );
}

// A session's rotation lineage is a list of its rotations, each its fields.
#[test]
fn a_sessions_rotations_are_their_proto3_json() {
    let s = v1::Session {
        rotations: vec![v1::SessionRotation {
            native_id: "id-1".to_string(),
            transcript_path: "/t/id-1.jsonl".to_string(),
            rotated_at: Some(
                Timestamp {
                    seconds: 1_784_309_252,
                    nanos: 0,
                }
                .into(),
            ),
        }],
        ..v1::Session::default()
    };
    assert_eq!(
        session(&s),
        json!({"rotations": [{
            "native_id": "id-1",
            "transcript_path": "/t/id-1.jsonl",
            "rotated_at": "2026-07-17T17:27:32Z",
        }]})
    );
}

// A removal's worktrees are the report, each checkout its fields.
#[test]
fn a_removals_worktrees_are_their_proto3_json() {
    let removal = v1::DeleteSessionResponse {
        worktrees: Some(v1::WorktreeReport {
            worktrees: vec![v1::ScratchWorktree {
                harp: "amber-swift-owl".to_string(),
                worktree: "ctxloom-wt-wip".to_string(),
                path: "/h/wt".to_string(),
                owner_state: "dead".to_string(),
                verdict: "spared".to_string(),
                reason: "uncommitted changes".to_string(),
            }],
            spared: 1,
            ..v1::WorktreeReport::default()
        }),
        ..v1::DeleteSessionResponse::default()
    };
    assert_eq!(
        deletion(&removal),
        json!({"worktrees": {
            "worktrees": [{
                "harp": "amber-swift-owl",
                "worktree": "ctxloom-wt-wip",
                "path": "/h/wt",
                "owner_state": "dead",
                "verdict": "spared",
                "reason": "uncommitted changes",
            }],
            "spared": "1",
        }})
    );
}

/// Every field of a session set, none at its default.
fn every_field_of_a_session() -> v1::Session {
    let at = |seconds, nanos| Some(daemon_proto::google::protobuf::Timestamp { seconds, nanos });
    v1::Session {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        uid: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b".to_string(),
        state: v1::session::State::Live as i32,
        engine: "claude-code".to_string(),
        summary: "fix the \"auth\" refactor\nand \u{fc}n\u{ef}code".to_string(),
        start_time: at(1_784_309_252, 481_000_000),
        end_time: at(1_784_309_999, 7),
        kept: true,
        owner: "projects/calm-otter/sessions/brisk-copper-moth".to_string(),
        closed: true,
        children: vec![
            "projects/calm-otter/sessions/a".to_string(),
            "b".to_string(),
        ],
        pending: vec![v1::PendingMessage {
            message_id: "m1".to_string(),
            urgency: "inform".to_string(),
            text: "hi".to_string(),
            sender: "projects/calm-otter/sessions/c".to_string(),
        }],
        incarnation: 4_294_967_295,
        etag: "e-1".to_string(),
        title: "the auth refactor".to_string(),
        native_session: "native-1".to_string(),
        project_root: "/home/a/p".to_string(),
        output_dir: "/home/a/p/ctxloom/amber-swift-owl".to_string(),
        rotations: vec![v1::SessionRotation {
            native_id: "id-1".to_string(),
            transcript_path: "/t/id-1.jsonl".to_string(),
            rotated_at: at(1_784_309_300, 481_200_000),
        }],
    }
}

/// Every field of a removal set but the needs, the etag's neighbours
/// `needs` and `notices` (nothing in process computes them, OQ-21's spike).
fn every_field_of_a_removal() -> v1::DeleteSessionResponse {
    v1::DeleteSessionResponse {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        authored: vec!["notes.md".to_string()],
        harp: "amber-swift-owl".to_string(),
        applied: true,
        files: vec![v1::RemovalFile {
            path: "/p/.ctxloom/sessions/amber-swift-owl/notes.md".to_string(),
            rel: "notes.md".to_string(),
            class: v1::removal_file::Class::Authored as i32,
            bytes: 18_446_744_073_709_551_615,
        }],
        bytes_freed: 4096,
        events_destroyed: 12,
        worktrees: Some(v1::WorktreeReport {
            worktrees: vec![v1::ScratchWorktree {
                harp: "amber-swift-owl".to_string(),
                worktree: "ctxloom-wt-wip".to_string(),
                path: "/h/wt".to_string(),
                owner_state: "dead".to_string(),
                verdict: "reaped".to_string(),
                reason: "clean".to_string(),
            }],
            reaped: 1,
            spared: 2,
            skipped: 3,
            applied: true,
        }),
        etag: "e-2".to_string(),
        ..v1::DeleteSessionResponse::default()
    }
}

/// `value` as `--format json` writes it: compact, and as the CLI prints it
/// (angzarr-presentation's `write_json`).
fn bytes_of<T: serde::Serialize>(value: &T) -> (String, String) {
    let mut printed = Vec::new();
    angzarr_presentation::write_json(&mut printed, value).expect("prints");
    (
        serde_json::to_string(value).expect("serializes"),
        String::from_utf8(printed).expect("utf-8"),
    )
}

/// The spike of OQ-21 (owner, 2026-11-13): the generated proto3 JSON
/// (pbjson-build) is BYTE-IDENTICAL to this module's hand-written renderer
/// on the same messages: every field set, every field at its default, and
/// the lists of each. The dev-dependency's `preserve_order` is the
/// binary's (engine-opencode's agent-client-protocol turns it on), so the
/// hand-written object keeps its insertion order here as it does there.
#[test]
fn test_the_generated_proto3_json_is_byte_identical_to_the_hand_written() {
    let session = every_field_of_a_session();
    let listed = v1::ListSessionsResponse {
        sessions: vec![session.clone(), v1::Session::default()],
        next_page_token: "t-1".to_string(),
    };
    let d = v1::Daemon {
        name: "daemon".to_string(),
        pid: 4242,
        endpoint: "https://127.0.0.1:5000".to_string(),
        start_time: Some(daemon_proto::google::protobuf::Timestamp {
            seconds: 1_784_309_252,
            nanos: 0,
        }),
        version: "v0.1.0-abc1234".to_string(),
        runs: vec![v1::HostedRun {
            session: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
            harp: "brisk-copper-moth".to_string(),
            run: "r-1".to_string(),
            kind: v1::hosted_run::Kind::Child as i32,
        }],
        clients: 3,
        idle_seconds: 1,
        idle_limit_seconds: 600,
    };
    let removal = every_field_of_a_removal();
    let sweep = v1::SweepSessionsResponse {
        sessions: vec![removal.clone()],
        rows: vec![v1::SweepRow {
            harp: "amber-swift-owl".to_string(),
            action: "purge".to_string(),
            verdict: "done".to_string(),
            reason: "older than 30 days".to_string(),
            command: "ctxloom session remove amber-swift-owl --yes".to_string(),
            bytes: 4096,
            authored: vec!["notes.md".to_string()],
        }],
        purge_cutoff: "2026-06-17T00:00:00Z".to_string(),
        counts: Some(v1::SweepCounts {
            done: 1,
            planned: 2,
            held: 3,
            left: 4,
            skipped: 5,
            failed: 6,
        }),
        etag: "e-3".to_string(),
        ..v1::SweepSessionsResponse::default()
    };
    let pairs = [
        (bytes_of(&session), bytes_of(&super::session(&session))),
        (
            bytes_of(&v1::Session::default()),
            bytes_of(&super::session(&v1::Session::default())),
        ),
        (bytes_of(&listed), bytes_of(&super::sessions(&listed))),
        (
            bytes_of(&v1::ListSessionsResponse::default()),
            bytes_of(&super::sessions(&v1::ListSessionsResponse::default())),
        ),
        (bytes_of(&d), bytes_of(&super::daemon(&d))),
        (
            bytes_of(&v1::Daemon::default()),
            bytes_of(&super::daemon(&v1::Daemon::default())),
        ),
        (bytes_of(&removal), bytes_of(&super::deletion(&removal))),
        (
            bytes_of(&v1::DeleteSessionResponse::default()),
            bytes_of(&super::deletion(&v1::DeleteSessionResponse::default())),
        ),
        (bytes_of(&sweep), bytes_of(&super::sweep(&sweep))),
        (
            bytes_of(&v1::SweepSessionsResponse::default()),
            bytes_of(&super::sweep(&v1::SweepSessionsResponse::default())),
        ),
    ];
    for (generated, hand_written) in pairs {
        assert_eq!(generated, hand_written);
    }
}
