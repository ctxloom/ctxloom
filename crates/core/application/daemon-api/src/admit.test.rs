use super::*;

fn caller(version: &str) -> v1::Caller {
    v1::Caller {
        client: Some(v1::Client {
            kind: v1::client::Kind::Hook as i32,
            program: "ctxloom".to_string(),
            program_dir: "/opt/ctxloom/bin".to_string(),
            version: version.to_string(),
        }),
        ..v1::Caller::default()
    }
}

fn mismatch(refused: Result<(), ApiError>) -> v1::ClientVersionMismatch {
    match refused {
        Err(ApiError::Refused(refused)) => refused
            .refusal
            .to_msg::<v1::ClientVersionMismatch>()
            .expect("a ClientVersionMismatch"),
        other => panic!("not refused: {other:?}"),
    }
}

#[test]
fn test_a_client_of_the_daemons_own_stamp_is_admitted() {
    assert!(admit("v1.2.3-abc", Some(&caller("v1.2.3-abc"))).is_ok());
}

#[test]
fn test_a_client_of_another_stamp_is_refused_naming_both_and_its_kind() {
    let refused = mismatch(admit("v1.2.3-abc", Some(&caller("v1.2.4-def"))));
    assert_eq!(refused.client_version, "v1.2.4-def");
    assert_eq!(refused.daemon_version, "v1.2.3-abc");
    assert_eq!(refused.kind, v1::client::Kind::Hook as i32);
}

#[test]
fn test_a_request_naming_no_client_is_refused_as_another_version() {
    let refused = mismatch(admit("v1.2.3-abc", None));
    assert_eq!(refused.client_version, "");
    assert_eq!(refused.kind, 0);
    let refused = mismatch(admit("v1.2.3-abc", Some(&v1::Caller::default())));
    assert_eq!(refused.client_version, "");
}

#[test]
fn test_a_launch_runs_the_clients_program_from_its_directory() {
    let caller = caller("v1");
    assert_eq!(
        program_of(caller.client.as_ref()),
        Some(daemon_application::Program {
            name: "ctxloom".to_string(),
            dir: "/opt/ctxloom/bin".to_string(),
        })
    );
}

#[test]
fn test_a_client_naming_no_program_leaves_the_daemons_own() {
    assert_eq!(program_of(None), None);
    let nameless = v1::Client {
        program_dir: "/somewhere".to_string(),
        ..v1::Client::default()
    };
    assert_eq!(program_of(Some(&nameless)), None);
}
