//! The stamping seam (D28 S-f). NEW-DESIGN.

use angzarr_embedded::v1::Uuid;
use sessions::{
    ClearChild, CloseSession, LaunchChild, SendMessage, SessionCommand, SessionRef, SetKept,
    StopSession, TargetTrust, Urgency,
};

use crate::identity::{Identity, Principal};
use crate::stamped;

fn forged() -> Option<SessionRef> {
    Some(SessionRef {
        project_id: "elsewhere".to_string(),
        session: "forged".to_string(),
    })
}

fn who() -> Identity {
    Identity {
        run: Uuid::from_bytes([1; 16]),
        session: Uuid::from_bytes([2; 16]),
        incarnation: 1,
        harp: "owl".to_string(),
        depth: 0,
        principal: Principal::Model,
    }
}

fn requester_of(command: &SessionCommand) -> Option<SessionRef> {
    match command {
        SessionCommand::CloseSession(c) => c.requester.clone(),
        SessionCommand::SendMessage(c) => c.requester.clone(),
        SessionCommand::ClearChild(c) => c.requester.clone(),
        SessionCommand::LaunchChild(c) => c.requester.clone(),
        SessionCommand::StopSession(c) => c.requester.clone(),
        _ => None,
    }
}

#[test]
fn the_requester_is_the_identitys_session_in_the_listeners_project() {
    assert_eq!(
        who().requester("calm-otter"),
        SessionRef {
            project_id: "calm-otter".to_string(),
            session: "02020202-0202-0202-0202-020202020202".to_string(),
        }
    );
}

// S-f: whatever requester a payload names, the credential's is stamped.
#[test]
fn every_guarded_command_is_stamped_with_the_credentials_requester() {
    let me = who().requester("calm-otter");
    let commands: Vec<SessionCommand> = vec![
        CloseSession {
            cause: "c".to_string(),
            requester: forged(),
        }
        .into(),
        SendMessage {
            message_id: "m".to_string(),
            urgency: Urgency::Wake,
            text: String::new(),
            requester: forged(),
            supported: Vec::new(),
        }
        .into(),
        ClearChild {
            clear_id: "c".to_string(),
            requester: None,
        }
        .into(),
        LaunchChild {
            child_project_id: String::new(),
            child_session: String::new(),
            child_root: String::new(),
            harp: String::new(),
            engine: String::new(),
            trust: TargetTrust {
                same_project: true,
                trusted: false,
            },
            delegation_limit: 1,
            requester: forged(),
        }
        .into(),
        StopSession {
            requester_depth: 0,
            reason: String::new(),
            requester: forged(),
        }
        .into(),
    ];
    for command in commands {
        let stamped = stamped(command, &me);
        assert_eq!(requester_of(&stamped), Some(me.clone()), "{stamped:?}");
    }
}

#[test]
fn a_command_the_owner_rule_does_not_guard_is_unchanged() {
    let keep: SessionCommand = SetKept { kept: true }.into();
    assert_eq!(stamped(keep.clone(), &who().requester("p")), keep);
}
