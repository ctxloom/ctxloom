//! A project's harp (owner S54): minted once, kept, and unique for the user.

use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::Cover;
use angzarr_embedded::{Failure, Registry};

use crate::{
    claim_root, project_name_root, project_namespace, project_root, ClaimProjectName, NameProject,
    ProjectAggregate, ProjectEvent, ProjectNameAggregate, ProjectNameClaimed, ProjectNameEvent,
    ProjectNamed, SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
};

const PROJECT: &str = "calm-otter";
const HARP: &str = "amber-swift-owl";

/// The domain's rejection a failure is, by its typed refusal's type.
fn refused<T: std::fmt::Debug>(result: Result<T, Failure<angzarr_embedded::Rejected>>) -> String {
    let Err(Failure::Rejected(rejected)) = result else {
        panic!("not a rejection: {result:?}");
    };
    let typed = rejected
        .rejection::<angzarr_embedded::markup::TypedRefusal>()
        .expect("a typed refusal");
    let rejection = SessionRejection::try_from(typed).expect("a refusal of this domain");
    angzarr_diagnostic::Diagnostic::code(&rejection).to_string()
}

fn project() -> Harness<ProjectAggregate> {
    let mut registry = Registry::new();
    registry
        .aggregate::<ProjectAggregate>(ProjectAggregate::default)
        .unwrap();
    let cover = Cover {
        domain: PROJECTS_DOMAIN.to_string(),
        root: Some(project_root(PROJECT)),
        edition: None,
    };
    Harness::new(registry, cover).with_samples(Samples::starting_at(100))
}

fn claim() -> Harness<ProjectNameAggregate> {
    let mut registry = Registry::new();
    registry
        .aggregate::<ProjectNameAggregate>(ProjectNameAggregate::default)
        .unwrap();
    let cover = Cover {
        domain: PROJECT_NAMES_DOMAIN.to_string(),
        root: Some(project_name_root(HARP)),
        edition: None,
    };
    Harness::new(registry, cover).with_samples(Samples::starting_at(100))
}

fn name(harp: &str) -> NameProject {
    NameProject {
        project_id: PROJECT.to_string(),
        harp: harp.to_string(),
    }
}

fn claim_for(project: &str) -> ClaimProjectName {
    ClaimProjectName {
        harp: HARP.to_string(),
        project_id: project.to_string(),
    }
}

// S54: a project is named once, with the harp asked.
#[test]
fn a_project_is_named_with_the_harp_asked() {
    let mut project = project();
    let events = project.when(name(HARP)).result().unwrap();
    assert_eq!(
        events,
        [ProjectEvent::ProjectNamed(ProjectNamed {
            project_id: PROJECT.to_string(),
            harp: HARP.to_string(),
        })]
    );
    assert_eq!(project.state().state().harp, HARP);
}

// S54 and S42: a harp is permanent; naming the project again records
// nothing, whatever harp is asked.
#[test]
fn a_named_project_keeps_its_harp_and_records_nothing_when_named_again() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    let before = project.pages().len();
    assert!(project
        .when(name("brisk-copper-moth"))
        .result()
        .unwrap()
        .is_empty());
    assert!(project.when(name(HARP)).result().unwrap().is_empty());
    assert_eq!(project.pages().len(), before);
    assert_eq!(project.state().state().harp, HARP);
}

// A harp that is no harp is refused as a session's is.
#[test]
fn a_project_named_with_no_harp_is_refused() {
    let mut project = project();
    assert_eq!(
        refused(project.when(name("")).result()),
        "sessions-harp-invalid"
    );
    assert!(project.pages().is_empty());
}

// S54: "its claim unique per user": a free harp is claimed by the first
// project to ask; its holder asking again records nothing; another project
// is refused.
#[test]
fn a_project_harp_is_held_by_one_project_for_the_user() {
    let mut names = claim();
    let events = names.when(claim_for(PROJECT)).result().unwrap();
    assert_eq!(
        events,
        [ProjectNameEvent::ProjectNameClaimed(ProjectNameClaimed {
            harp: HARP.to_string(),
            project_id: PROJECT.to_string(),
        })]
    );
    let before = names.pages().len();
    assert!(names.when(claim_for(PROJECT)).result().unwrap().is_empty());
    assert_eq!(
        refused(names.when(claim_for("far-heron")).result()),
        "sessions-project-name-taken"
    );
    assert_eq!(names.pages().len(), before);
    assert_eq!(names.state().state().project_id, PROJECT);
}

// The roots: a project's stream is its namespace, which no claim or imported
// session in it can be; a project harp's claim is one root per harp, the
// same whichever project asks, and never a session name claim's.
#[test]
fn the_roots_are_fixed_by_the_project_and_the_harp() {
    let namespace = project_namespace(PROJECT);
    assert_eq!(project_root(PROJECT).value, namespace.as_bytes().to_vec());
    assert_ne!(project_root(PROJECT), project_root("far-heron"));
    assert_ne!(project_root(PROJECT), claim_root(PROJECT, HARP));
    assert_eq!(project_name_root(HARP), project_name_root(HARP));
    assert_ne!(
        project_name_root(HARP),
        project_name_root("brisk-copper-moth")
    );
    assert_ne!(project_name_root(HARP), claim_root(PROJECT, HARP));
}

// FIXED: a project harp's claim root is UUIDv5 of the harp in the namespace
// UUIDv5(URL, "ctxloom:project-names"); changing it frees every harp
// claimed.
#[test]
fn a_project_harps_claim_root_is_fixed() {
    let namespace = uuid::Uuid::new_v5(&uuid::Uuid::NAMESPACE_URL, b"ctxloom:project-names");
    let expected = uuid::Uuid::new_v5(&namespace, HARP.as_bytes());
    assert_eq!(project_name_root(HARP).value, expected.as_bytes().to_vec());
}

// The states round-trip through their wire messages.
#[test]
fn the_project_states_round_trip_through_the_wire() {
    use crate::{ProjectNameState, ProjectState};
    let project = ProjectState {
        project_id: PROJECT.to_string(),
        harp: HARP.to_string(),
    };
    let wire = sessions_proto::v1::ProjectState::from(project.clone());
    assert_eq!(wire.project_id, PROJECT);
    assert_eq!(wire.harp, HARP);
    assert_eq!(ProjectState::try_from(wire).unwrap(), project);
    let claim = ProjectNameState {
        harp: HARP.to_string(),
        project_id: PROJECT.to_string(),
    };
    let wire = sessions_proto::v1::ProjectNameState::from(claim.clone());
    assert_eq!(
        (wire.harp.as_str(), wire.project_id.as_str()),
        (HARP, PROJECT)
    );
    assert_eq!(ProjectNameState::try_from(wire).unwrap(), claim);
}
