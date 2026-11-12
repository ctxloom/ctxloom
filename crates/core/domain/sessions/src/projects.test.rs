//! A project's harp (owner S54): minted once, kept, and unique for the user.

use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::Cover;
use angzarr_embedded::{Failure, Registry};

use crate::{
    project_id_root, ClaimProjectName, NameProject, ProjectAggregate, ProjectEvent, ProjectName,
    ProjectNameAggregate, ProjectNameClaimed, ProjectNameEvent, ProjectNamed, ProjectRegistered,
    RegisterProject, SessionName, SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
};

const PROJECT: &str = "calm-otter";
const HARP: &str = "amber-swift-owl";
const ROOT: &str = "/work/calm";
const COMMON: &str = "/work/calm/.git";
const COMMIT: &str = "1111111111111111111111111111111111111111";

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
        root: Some(project_id_root(PROJECT)),
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
        root: Some(ProjectName::new(HARP).unwrap().root()),
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

fn register(commit: &str, expected: &str) -> RegisterProject {
    RegisterProject {
        project_id: PROJECT.to_string(),
        root: ROOT.to_string(),
        git_common_dir: COMMON.to_string(),
        root_commit: commit.to_string(),
        expected_harp: expected.to_string(),
    }
}

/// The rejection a failure is, whole.
fn rejection<T: std::fmt::Debug>(
    result: Result<T, Failure<angzarr_embedded::Rejected>>,
) -> SessionRejection {
    let Err(Failure::Rejected(rejected)) = result else {
        panic!("not a rejection: {result:?}");
    };
    let typed = rejected
        .rejection::<angzarr_embedded::markup::TypedRefusal>()
        .expect("a typed refusal");
    SessionRejection::try_from(typed).expect("a refusal of this domain")
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

// The roots (owner, 2026-11-12, slice 2 re-root): a project's stream is
// keyed by its id, a project harp's claim by the project's resource name,
// one per harp whichever project asks; neither is a session's.
#[test]
fn the_roots_are_the_project_id_and_the_projects_name() {
    let id = uuid::Uuid::new_v5(&crate::NAMESPACE, PROJECT.as_bytes());
    assert_eq!(project_id_root(PROJECT).value, id.as_bytes().to_vec());
    assert_ne!(project_id_root(PROJECT), project_id_root("far-heron"));
    let claim = ProjectName::new(HARP).unwrap().root();
    let expected = uuid::Uuid::new_v5(&crate::NAMESPACE, b"projects/amber-swift-owl");
    assert_eq!(claim.value, expected.as_bytes().to_vec());
    assert_ne!(claim, ProjectName::new("brisk-copper-moth").unwrap().root());
    assert_ne!(claim, SessionName::new(HARP, HARP).unwrap().root());
}

// Project registration (owner, 2026-11-12): the first registration of a
// named project records its root and VCS identity.
#[test]
fn a_named_project_is_registered_on_first_sight() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    let events = project.when(register(COMMIT, "")).result().unwrap();
    assert_eq!(
        events,
        [ProjectEvent::ProjectRegistered(ProjectRegistered {
            project_id: PROJECT.to_string(),
            root: ROOT.to_string(),
            git_common_dir: COMMON.to_string(),
            root_commit: COMMIT.to_string(),
        })]
    );
    let state = project.state().state().clone();
    assert_eq!(
        (state.root.as_str(), state.git_common_dir.as_str()),
        (ROOT, COMMON)
    );
    assert_eq!(state.root_commit, COMMIT);
    assert_eq!(state.harp, HARP, "the harp stays");
}

// A recheck with the same identity, and the harp expected, records nothing;
// so does one from a worktree (another root, the same repository).
#[test]
fn a_recheck_that_matches_records_nothing() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    project.when(register(COMMIT, "")).result().unwrap();
    let before = project.pages().len();
    assert!(project
        .when(register(COMMIT, HARP))
        .result()
        .unwrap()
        .is_empty());
    let worktree = RegisterProject {
        root: "/work/calm-wt".to_string(),
        ..register(COMMIT, "")
    };
    assert!(project.when(worktree).result().unwrap().is_empty());
    assert_eq!(project.pages().len(), before);
    assert_eq!(project.state().state().root, ROOT, "the first root stays");
}

// A changed root commit is another repository at the project's place:
// refused, naming the registered project, recording nothing.
#[test]
fn a_recheck_whose_root_commit_changed_is_refused_naming_the_project() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    project.when(register(COMMIT, "")).result().unwrap();
    let before = project.pages().len();
    let other = "2222222222222222222222222222222222222222";
    assert_eq!(
        rejection(project.when(register(other, "")).result()),
        SessionRejection::ProjectIdentityChanged {
            project: "projects/amber-swift-owl".to_string(),
            root: ROOT.to_string(),
            registered_commit: COMMIT.to_string(),
            found_commit: other.to_string(),
        }
    );
    assert_eq!(project.pages().len(), before);
}

// A client that expects another harp is refused, naming the registered
// project; at the first registration too.
#[test]
fn a_harp_mismatch_is_refused_naming_the_project() {
    let mismatch = SessionRejection::ProjectHarpMismatch {
        project: "projects/amber-swift-owl".to_string(),
        expected: "far-heron".to_string(),
    };
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    assert_eq!(
        rejection(project.when(register(COMMIT, "far-heron")).result()),
        mismatch
    );
    project.when(register(COMMIT, "")).result().unwrap();
    assert_eq!(
        rejection(project.when(register(COMMIT, "far-heron")).result()),
        mismatch
    );
}

// A project not yet named cannot be registered: its harp is what a refusal
// names.
#[test]
fn an_unnamed_project_is_not_registered() {
    let mut project = project();
    assert_eq!(
        refused(project.when(register(COMMIT, "")).result()),
        "sessions-project-not-named"
    );
    assert!(project.pages().is_empty());
}

// Outside git (an empty VCS identity, owner question): registered with
// empty identity fields, and a later recheck with the same empties matches.
#[test]
fn a_project_outside_git_is_registered_with_an_empty_identity() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    let outside = RegisterProject {
        git_common_dir: String::new(),
        ..register("", "")
    };
    assert_eq!(project.when(outside.clone()).result().unwrap().len(), 1);
    assert!(project.when(outside).result().unwrap().is_empty());
    assert_eq!(project.state().state().root_commit, "");
}

// The states round-trip through their wire messages.
#[test]
fn the_project_states_round_trip_through_the_wire() {
    use crate::{ProjectNameState, ProjectState};
    let project = ProjectState {
        project_id: PROJECT.to_string(),
        harp: HARP.to_string(),
        root: ROOT.to_string(),
        git_common_dir: COMMON.to_string(),
        root_commit: COMMIT.to_string(),
    };
    let wire = sessions_proto::v1::ProjectState::from(project.clone());
    assert_eq!(wire.project_id, PROJECT);
    assert_eq!(wire.harp, HARP);
    assert_eq!(
        (
            wire.root.as_str(),
            wire.git_common_dir.as_str(),
            wire.root_commit.as_str()
        ),
        (ROOT, COMMON, COMMIT)
    );
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
