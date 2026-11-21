//! A project's harp (owner S54): minted once, kept, and unique for the user.

use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::Cover;
use angzarr_embedded::{Failure, Registry};

use crate::{
    project_id_root, ClaimProjectName, NameProject, ProjectAggregate, ProjectEvent,
    ProjectIdentityAdopted, ProjectName, ProjectNameAggregate, ProjectNameClaimed,
    ProjectNameEvent, ProjectNamed, ProjectRegistered, RegisterProject, SessionName,
    SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
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

// Owner, 2026-11-12 (after step 5): a project registered outside git ADOPTS
// the first VCS identity it is seen with, recorded as an event; its root
// stays the one registered, and the comparison is strict from then on.
#[test]
fn a_project_registered_outside_git_adopts_its_first_identity_then_is_strict() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    let outside = RegisterProject {
        git_common_dir: String::new(),
        ..register("", "")
    };
    project.when(outside.clone()).result().unwrap();
    let inside = RegisterProject {
        root: "/work/calm-wt".to_string(),
        ..register(COMMIT, HARP)
    };
    assert_eq!(
        project.when(inside.clone()).result().unwrap(),
        [ProjectEvent::ProjectIdentityAdopted(
            ProjectIdentityAdopted {
                project_id: PROJECT.to_string(),
                git_common_dir: COMMON.to_string(),
                root_commit: COMMIT.to_string(),
            }
        )]
    );
    let state = project.state().state().clone();
    assert_eq!(
        (
            state.root.as_str(),
            state.git_common_dir.as_str(),
            state.root_commit.as_str()
        ),
        (ROOT, COMMON, COMMIT),
        "the root stays the registered one"
    );
    let before = project.pages().len();
    assert!(project.when(inside).result().unwrap().is_empty());
    let other = "2222222222222222222222222222222222222222";
    for found in [other, ""] {
        let changed = RegisterProject {
            git_common_dir: String::new(),
            ..register(found, "")
        };
        assert_eq!(
            rejection(project.when(changed).result()),
            SessionRejection::ProjectIdentityChanged {
                project: "projects/amber-swift-owl".to_string(),
                root: ROOT.to_string(),
                registered_commit: COMMIT.to_string(),
                found_commit: found.to_string(),
            },
            "strict after the adoption: {found:?}"
        );
    }
    assert_eq!(project.pages().len(), before, "and nothing recorded");
}

// A git common dir with no root commit (a repository with no commit yet) is
// no identity to adopt: it matches the empty one, recording nothing.
#[test]
fn a_repository_with_no_commit_yet_is_no_identity_to_adopt() {
    let mut project = project();
    project.when(name(HARP)).result().unwrap();
    project
        .when(RegisterProject {
            git_common_dir: String::new(),
            ..register("", "")
        })
        .result()
        .unwrap();
    assert!(project.when(register("", "")).result().unwrap().is_empty());
    assert_eq!(project.state().state().git_common_dir, "");
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
        ..ProjectState::default()
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

// ------------------------------------------------- a session's removal (O2)

/// Evidence of a session whose removal nothing refuses.
fn removable(harp: &str) -> crate::RemovalEvidence {
    crate::RemovalEvidence {
        harp: harp.to_string(),
        ended: true,
        not_ended: String::new(),
        kept: false,
        retained: String::new(),
        spared: 0,
        skipped: 0,
        blocking_lock: String::new(),
    }
}

fn remove(evidence: crate::RemovalEvidence, even_if_live: bool) -> crate::RemoveSession {
    crate::RemoveSession {
        project: PROJECT.to_string(),
        harp: evidence.harp.clone(),
        even_if_live,
        evidence,
    }
}

/// Record a removal's step done, as the daemon does: a fact keyed
/// `{request_id}:{step}`. What it recorded.
fn step_done(project: &mut Harness<ProjectAggregate>, request_id: &str, step: u32) -> usize {
    use angzarr_embedded::markup::DomainMessage;
    use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Fact, FactOrigin};
    let fact = crate::RecordRemovalStepDone {
        request_id: request_id.to_string(),
        step,
    };
    let book = CommandBook {
        cover: Some(project.cover().clone()),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(angzarr_embedded::Any::from_msg(&fact.to_wire()).unwrap()),
                origin: Some(FactOrigin {
                    external_id: format!("removal:{request_id}:{step}"),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    };
    match project
        .when_book(book)
        .handled()
        .expect("a fact is never refused")
    {
        angzarr_embedded::Handled::Applied(recorded) => {
            recorded.books().iter().map(|book| book.pages.len()).sum()
        }
        _ => 0,
    }
}

// Step 10b O2: a session's removal is a command of the PROJECT stream, its
// four steps listed in its intent under its request id (the command's id),
// and kept open until each is recorded done, in order; then nothing of it
// is left.
#[test]
fn a_removal_is_intended_with_its_steps_and_open_until_each_is_done() {
    let mut project = project();
    project.when(name(PROJECT)).result().unwrap();
    let events = project
        .when(remove(removable(HARP), false))
        .result()
        .unwrap();
    let [ProjectEvent::SessionRemovalIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(intended.harp, HARP);
    assert_eq!(
        intended.steps,
        ["worktrees", "trash", "purge", "purge_trash"].map(String::from)
    );
    assert!(!intended.request_id.is_empty(), "the command's own id");
    let id = intended.request_id.clone();
    let open = |project: &Harness<ProjectAggregate>| {
        project
            .state()
            .state()
            .removals
            .get(&id)
            .map(|removal| removal.done)
    };
    assert_eq!(open(&project), Some(0));
    assert_eq!(step_done(&mut project, &id, 1), 0, "not the next step");
    assert_eq!(step_done(&mut project, &id, 0), 1);
    assert_eq!(open(&project), Some(1));
    assert_eq!(step_done(&mut project, "another", 1), 0, "no such removal");
    for step in 1..4 {
        assert_eq!(step_done(&mut project, &id, step), 1, "step {step}");
    }
    assert_eq!(open(&project), None, "a removal done is dropped");
}

// D18: where a held lock keeps the directory from being moved (the
// evidence names it, on Windows), `--even-if-live` is refused up front,
// naming the lock, and nothing is intended; without the flag the lock's
// own refusal comes first, as on unix.
#[test]
fn a_removal_whose_held_lock_blocks_the_move_is_refused_even_if_live() {
    let mut project = project();
    project.when(name(PROJECT)).result().unwrap();
    let blocked = crate::RemovalEvidence {
        ended: false,
        not_ended: "its lock is held".to_string(),
        blocking_lock: "C:/p/.ctxloom/sessions/h/session.lock".to_string(),
        ..removable(HARP)
    };
    let Err(Failure::Rejected(rejected)) = project.when(remove(blocked.clone(), true)).result()
    else {
        panic!("not refused")
    };
    let typed = rejected
        .rejection::<angzarr_embedded::markup::TypedRefusal>()
        .expect("a typed refusal");
    assert_eq!(
        SessionRejection::try_from(typed),
        Ok(SessionRejection::SessionLockHeld {
            harp: HARP.to_string(),
            lock: "C:/p/.ctxloom/sessions/h/session.lock".to_string(),
        })
    );
    assert_eq!(
        refused(project.when(remove(blocked.clone(), true)).result()),
        "sessions-session-lock-held"
    );
    assert_eq!(
        refused(project.when(remove(blocked, false)).result()),
        "sessions-session-live"
    );
    assert!(project.state().state().removals.is_empty());
}

// The removal is judged on its evidence, in today's order: the lock (unless
// --even-if-live), keep, the retention, then the worktrees.
#[test]
fn a_removal_is_refused_on_its_evidence() {
    let mut project = project();
    project.when(name(PROJECT)).result().unwrap();
    let live = crate::RemovalEvidence {
        ended: false,
        not_ended: "its lock is held".to_string(),
        ..removable(HARP)
    };
    assert_eq!(
        refused(project.when(remove(live.clone(), false)).result()),
        "sessions-session-live"
    );
    let kept = crate::RemovalEvidence {
        kept: true,
        ..live.clone()
    };
    assert_eq!(
        refused(project.when(remove(kept, true)).result()),
        "sessions-session-kept",
        "--even-if-live passes the lock, never keep"
    );
    let retained = crate::RemovalEvidence {
        retained: "it ended an hour ago".to_string(),
        ..removable(HARP)
    };
    assert_eq!(
        refused(project.when(remove(retained, false)).result()),
        "sessions-within-retention"
    );
    let spared = crate::RemovalEvidence {
        spared: 1,
        ..removable(HARP)
    };
    assert_eq!(
        refused(project.when(remove(spared, false)).result()),
        "sessions-keeps-session"
    );
    assert!(
        project.state().state().removals.is_empty(),
        "nothing refused is intended"
    );
    let anyway = project.when(remove(live, true)).result().unwrap();
    assert_eq!(anyway.len(), 1, "--even-if-live removes it anyway");
}

// The open removals round-trip through the state's wire message.
#[test]
fn the_open_removals_round_trip_through_the_wire() {
    use crate::{OpenRemoval, ProjectState};
    let mut removals = std::collections::BTreeMap::new();
    removals.insert(
        "r-1".to_string(),
        OpenRemoval {
            request_id: "r-1".to_string(),
            harp: HARP.to_string(),
            steps: vec!["trash".to_string()],
            done: 1,
        },
    );
    let project = ProjectState {
        project_id: PROJECT.to_string(),
        removals,
        ..ProjectState::default()
    };
    let wire = sessions_proto::v1::ProjectState::from(project.clone());
    assert_eq!(wire.removals["r-1"].done, 1);
    assert_eq!(ProjectState::try_from(wire).unwrap(), project);
}
