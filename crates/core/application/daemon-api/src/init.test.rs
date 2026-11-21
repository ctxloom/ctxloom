//! `InitProject`'s pieces (step 10c U6): its needs as the preview asks them,
//! its answers as the apply reads them, the checkout's harp staged with the
//! scaffold, and a retry's tree found in its intent.

use daemon_proto::v1;
use init_application::needs::{self, Given};
use library_api::writes::Shape;
use library_tree::TreeName;

use super::{
    companion_answers, derived_id, error_notice, given_of, installed_of, need_of, project_of,
    tree_of, with_checkout,
};
use crate::refusal::{ApiError, ApiRefusal};

#[test]
fn test_every_need_init_asks_is_optional_and_keeps_its_kind() {
    let asked: Vec<v1::Need> = needs::needs(&["mempalace".to_string()])
        .iter()
        .map(need_of)
        .collect();
    assert!(asked.iter().all(|need| need.optional), "{asked:?}");
    assert!(matches!(
        &asked[0].kind,
        Some(v1::need::Kind::Ask(ask)) if ask.suggestion == "ctxloom/" && ask.text == init_application::OUTPUT_DIR_QUESTION
    ));
    assert!(matches!(
        &asked[1].kind,
        Some(v1::need::Kind::Choose(choose)) if choose.choices == ["on-demand", "always"]
    ));
    assert!(matches!(
        &asked[2].kind,
        Some(v1::need::Kind::Confirm(confirm)) if confirm.text == "Add the mempalace companion?"
    ));
    assert_eq!(asked[2].id, "companion:mempalace");
}

#[test]
fn test_the_answers_are_read_by_their_need_and_kind() {
    let answer = |id: &str, answer: v1::answer::Answer| v1::Answer {
        need_id: id.to_string(),
        answer: Some(answer),
    };
    let given = given_of(&[
        answer("output-dir", v1::answer::Answer::Text("out/".to_string())),
        answer("premised", v1::answer::Answer::Chosen("always".to_string())),
        answer("companion:x", v1::answer::Answer::Confirmed(true)),
        v1::Answer {
            need_id: "unanswered".to_string(),
            answer: None,
        },
    ]);
    assert_eq!(
        given,
        [
            ("output-dir".to_string(), Given::Text("out/".to_string())),
            ("premised".to_string(), Given::Chosen("always".to_string())),
            ("companion:x".to_string(), Given::Confirmed(true)),
        ]
    );
}

#[test]
fn test_the_checkout_harp_is_staged_with_the_scaffold() {
    let shape = with_checkout(
        Shape::Create {
            path: String::new(),
            files: vec![ctxloom_ports::Put::file("config.yaml", "a")],
        },
        "brisk-copper-moth",
    );
    let Shape::Create { path, files } = shape else {
        panic!("{shape:?}")
    };
    assert_eq!(path, "");
    let staged: Vec<(&str, &[u8])> = files
        .iter()
        .map(|put| (put.path.as_str(), put.bytes.as_slice()))
        .collect();
    assert_eq!(
        staged,
        [
            ("config.yaml", b"a".as_slice()),
            ("state/checkout", b"brisk-copper-moth\n".as_slice()),
        ]
    );
}

#[test]
fn test_a_retry_finds_its_tree_in_its_intent() {
    let name = "projects/calm-otter/checkouts/brisk-copper-moth/library";
    let known = library_checkout::Known {
        root: Vec::new(),
        intent: intent(vec![library_tree::Step {
            create: Some(library_tree::CreateDirectory {
                name: name.to_string(),
                verb: "init".to_string(),
                path: String::new(),
                entries: Vec::new(),
                evidence: library_tree::Evidence {
                    tree_reads: true,
                    plan_rejection: None,
                    builtin: false,
                },
            }),
            replace: None,
            trash: None,
            r#move: None,
            purge: None,
        }]),
        end: library_checkout::End::Pending { done: 0 },
    };
    assert_eq!(
        tree_of(&known).map(|tree| tree.to_string()).as_deref(),
        Some(name)
    );
    assert_eq!(
        tree_of(&known).as_ref().map(TreeName::checkout_harp),
        Some("brisk-copper-moth")
    );
    let none = library_checkout::Known {
        intent: intent(Vec::new()),
        ..known
    };
    assert_eq!(tree_of(&none), None);
}

fn intent(steps: Vec<library_tree::Step>) -> library_tree::Intent {
    library_tree::Intent {
        request_id: "r".to_string(),
        verb: "init".to_string(),
        steps,
        base_cause: String::new(),
        done: 0,
        foreign: Vec::new(),
    }
}

// `manage install` answers the project's own `.gitignore` beside its
// `.ctxloom`, and what it did.
#[test]
fn test_manage_install_answers_the_project_s_gitignore_and_what_it_did() {
    let made = installed_of(
        "/work/calm/.ctxloom",
        (true, "unchanged".to_string()),
        "calm-otter".to_string(),
        Vec::new(),
    );
    assert_eq!(made.gitignore, "/work/calm/.gitignore");
    assert_eq!(made.app_dir, "/work/calm/.ctxloom");
    assert!(made.initialized);
    assert_eq!(made.gitignore_status, "unchanged");
    assert_eq!(made.project_id, "calm-otter");
}

// Each write init makes beside the project is keyed by the init's own request
// id, so its retry finds each as it left it; none where the init has none.
#[test]
fn test_a_write_beside_the_project_is_keyed_by_the_inits_request_id() {
    let init = "0192c3a0-7b1e-7a10-8a3e-5d2f4c6b8a90";
    let remote = derived_id(init, "remote:personal");
    assert!(uuid::Uuid::parse_str(&remote).is_ok(), "{remote}");
    assert_eq!(remote, derived_id(init, "remote:personal"), "stable");
    assert_ne!(remote, derived_id(init, "remote:personal-2"));
    assert_ne!(remote, derived_id(init, "companion:personal"));
    assert_ne!(remote, init);
    assert_ne!(
        remote,
        derived_id("0192c3a0-7b1e-7a10-8a3e-5d2f4c6b8a91", "remote:personal")
    );
    assert_eq!(derived_id("", "remote:personal"), "");
    assert_eq!(derived_id("not-a-uuid", "remote:personal"), "");
}

#[test]
fn test_the_companion_answers_are_read_as_init_application_reads_them() {
    let answer = |id: &str, answer: Option<v1::answer::Answer>| v1::Answer {
        need_id: id.to_string(),
        answer,
    };
    let read = companion_answers(&[
        answer("companion:ltk", Some(v1::answer::Answer::Confirmed(true))),
        answer(
            "companion:reprise",
            Some(v1::answer::Answer::Confirmed(false)),
        ),
        answer(
            "companions",
            Some(v1::answer::Answer::Text("acme beta".to_string())),
        ),
        answer(
            "premised",
            Some(v1::answer::Answer::Chosen("always".to_string())),
        ),
    ]);
    let given: Vec<(&str, Option<&str>)> = read
        .iter()
        .map(|a| (a.id.as_str(), a.given.as_deref()))
        .collect();
    assert_eq!(
        given,
        [
            ("companion:ltk", Some("yes")),
            ("companion:reprise", Some("no")),
            ("companions", Some("acme beta")),
            ("premised", None),
        ]
    );
    assert_eq!(
        init_application::accepted_companions(&read),
        ["ltk", "acme", "beta"]
    );
}

// A write that would not register is said in its own words: a declined
// write by its cause's code and args, a failure by the daemon's.
#[test]
fn test_a_write_init_could_not_make_is_said_in_its_own_words() {
    let declined = ApiError::Refused(ApiRefusal::of(&v1::LibraryDeclined {
        cause: Some(v1::Cause {
            code: "library-application-write-companion-program-missing".to_string(),
            args: vec![v1::NeedArg {
                name: "program".to_string(),
                value: "ctxloom-companion-x".to_string(),
            }],
        }),
    }));
    let notice = error_notice(&declined);
    assert_eq!(
        notice.code,
        "library-application-write-companion-program-missing"
    );
    assert_eq!(notice.args.len(), 1, "{notice:?}");
    assert_eq!(notice.args[0].value, "ctxloom-companion-x");
    let failed = error_notice(&ApiError::Failed {
        detail: "disk full".to_string(),
    });
    assert_eq!(failed.code, "daemon-api-failed");
    assert!(failed.text.contains("disk full"), "{failed:?}");
}

#[test]
fn test_the_project_a_tree_is_of_is_its_resource_name() {
    let tree =
        TreeName::parse_checkout("projects/amber-quiet-heron/checkouts/brisk-copper-moth/library")
            .expect("a checkout tree");
    assert_eq!(project_of(&tree), "projects/amber-quiet-heron");
}
