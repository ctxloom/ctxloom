//! The resource names and the roots they derive (owner, 2026-11-12, slice 2
//! B3/re-root).

use super::*;
use crate::SessionRejection;

fn malformed(name: &str, reason: &str) -> SessionRejection {
    SessionRejection::NameMalformed {
        name: name.to_string(),
        reason: reason.to_string(),
    }
}

#[test]
fn the_namespace_is_the_one_minted_and_never_changes() {
    // Every stream's root is derived from it: a change orphans every stream
    // in every store.
    assert_eq!(
        NAMESPACE.hyphenated().to_string(),
        "1473f25e-0851-4e01-8741-81055551414f"
    );
}

#[test]
fn a_project_name_is_projects_and_its_harp() {
    let name = ProjectName::new("calm-otter").unwrap();
    assert_eq!(name.to_string(), "projects/calm-otter");
    assert_eq!(name.harp(), "calm-otter");
}

#[test]
fn a_session_name_is_its_projects_harp_and_its_own() {
    let name = SessionName::new("calm-otter", "amber-swift-owl").unwrap();
    assert_eq!(
        name.to_string(),
        "projects/calm-otter/sessions/amber-swift-owl"
    );
    assert_eq!(name.project_harp(), "calm-otter");
    assert_eq!(name.harp(), "amber-swift-owl");
    assert_eq!(name.project(), ProjectName::new("calm-otter").unwrap());
    assert_eq!(
        ProjectName::new("calm-otter")
            .unwrap()
            .session("amber-swift-owl")
            .unwrap(),
        name
    );
}

#[test]
fn a_name_parses_back_to_what_formatted_it() {
    let session = SessionName::new("calm-otter", "amber-swift-owl").unwrap();
    assert_eq!(SessionName::parse(&session.to_string()), Ok(session));
    let project = ProjectName::new("calm-otter").unwrap();
    assert_eq!(ProjectName::parse(&project.to_string()), Ok(project));
}

#[test]
fn a_harp_that_is_no_harp_is_refused_as_the_harp_grammar_refuses_it() {
    for bad in ["", "..", "a:b", " lead"] {
        assert_eq!(
            ProjectName::new(bad),
            Err(crate::validate(bad).unwrap_err()),
            "{bad:?}"
        );
        assert_eq!(
            SessionName::new("calm-otter", bad),
            Err(crate::validate(bad).unwrap_err()),
            "{bad:?}"
        );
        assert_eq!(
            SessionName::new(bad, "amber-swift-owl"),
            Err(crate::validate(bad).unwrap_err()),
            "{bad:?}"
        );
    }
}

#[test]
fn the_harp_grammar_is_the_sessions_not_aip_122s_charset() {
    // Go's harp is permissive on charset: what is a path component is one.
    let name = SessionName::parse("projects/Calm_Otter/sessions/release notes").unwrap();
    assert_eq!(name.project_harp(), "Calm_Otter");
    assert_eq!(name.harp(), "release notes");
}

#[test]
fn a_malformed_name_is_refused_saying_why() {
    for (bad, reason) in [
        ("", "shape"),
        ("calm-otter", "shape"),
        ("project/calm-otter", "shape"),
        ("projects/calm-otter", "shape"),
        ("projects/calm-otter/session/owl", "shape"),
        ("projects/calm-otter/sessions", "shape"),
        ("projects/calm-otter/sessions/owl/extra", "shape"),
        ("projects//sessions/owl", "empty"),
        ("projects/calm-otter/sessions/", "empty"),
        ("projects/calm-otter/sessions/..", "directory"),
        ("projects/calm-otter/sessions/ owl", "whitespace"),
        ("projects/calm-otter/sessions/o\tl", "control"),
    ] {
        assert_eq!(
            SessionName::parse(bad),
            Err(malformed(bad, reason)),
            "{bad:?}"
        );
    }
    for (bad, reason) in [
        ("", "shape"),
        ("calm-otter", "shape"),
        ("projects/", "empty"),
        ("projects/calm-otter/sessions/owl", "shape"),
        ("project/calm-otter", "shape"),
        ("projects/a:b", "separator"),
    ] {
        assert_eq!(
            ProjectName::parse(bad),
            Err(malformed(bad, reason)),
            "{bad:?}"
        );
    }
}

#[test]
fn a_root_is_the_uuid_v5_of_the_name_in_the_namespace() {
    let session = SessionName::new("calm-otter", "amber-swift-owl").unwrap();
    let expected = uuid::Uuid::new_v5(&NAMESPACE, b"projects/calm-otter/sessions/amber-swift-owl");
    assert_eq!(session.id(), expected);
    assert_eq!(session.uid(), expected.hyphenated().to_string());
    assert_eq!(session.root().value, expected.as_bytes().to_vec());
    assert_eq!(root_of(&session.to_string()), expected);

    let project = ProjectName::new("calm-otter").unwrap();
    let expected = uuid::Uuid::new_v5(&NAMESPACE, b"projects/calm-otter");
    assert_eq!(project.id(), expected);
    assert_eq!(project.root().value, expected.as_bytes().to_vec());
}

#[test]
fn a_root_differs_by_project_and_by_session() {
    let owl = SessionName::new("calm-otter", "amber-swift-owl").unwrap();
    let moth = SessionName::new("calm-otter", "brisk-copper-moth").unwrap();
    let elsewhere = SessionName::new("far-heron", "amber-swift-owl").unwrap();
    assert_ne!(owl.id(), moth.id());
    assert_ne!(owl.id(), elsewhere.id());
    assert_ne!(owl.id(), owl.project().id());
}

#[test]
fn the_project_id_root_is_the_uuid_v5_of_the_id_in_the_namespace() {
    // A project id holds no `/`, so its root is never a resource name's.
    let expected = uuid::Uuid::new_v5(&NAMESPACE, b"high-tight-gulf");
    assert_eq!(
        project_id_root("high-tight-gulf").value,
        expected.as_bytes().to_vec()
    );
    assert_ne!(
        project_id_root("calm-otter").value,
        ProjectName::new("calm-otter").unwrap().root().value
    );
}
