//! Each served write's use case and response.

use daemon_proto::v1;
use library_application::fakes::{plain, Setup, CONFIG, ENVELOPE};
use library_application::BundleCreated;

use super::{create_bundle, Wrote};

fn wrote<T>(answer: Option<T>) -> Wrote<T> {
    Wrote {
        answer,
        applied: true,
        etag: "4".to_string(),
        at: "/work/demo/.ctxloom/content/bundles/v2/notes".to_string(),
        parent: "projects/amber-quiet-heron".to_string(),
        notices: Vec::new(),
        source: String::new(),
    }
}

fn create_request() -> v1::CreateBundleRequest {
    v1::CreateBundleRequest {
        bundle_id: "notes".to_string(),
        bundle: Some(v1::Bundle {
            description: "my notes".to_string(),
            ..v1::Bundle::default()
        }),
        ..v1::CreateBundleRequest::default()
    }
}

#[test]
fn test_a_bundle_create_plans_the_use_cases_bundle_with_its_description() {
    let (library, _) = Setup {
        bundles: &[("demo", &[ENVELOPE])],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture();
    let (recording, recorder) = library.recording();
    let made = create_bundle::plan(&recording, &plain(), &create_request(), true)
        .result
        .unwrap();
    assert_eq!(made.name, "notes");
    let planned = recorder.take();
    let library_application::write::recording::Planned::NewBundle { files, .. } = &planned[0]
    else {
        panic!("{planned:?}")
    };
    let envelope = files.iter().find(|f| f.path == "bundle.yaml").unwrap();
    assert!(String::from_utf8_lossy(&envelope.bytes).contains("my notes"));
}

#[test]
fn test_a_bundle_create_answers_its_bundle_path_and_etag_done_or_retried() {
    let made = BundleCreated {
        status: "created".to_string(),
        name: "notes".to_string(),
        path: "/made/bundle.yaml".to_string(),
    };
    let answered = create_bundle::respond(&create_request(), wrote(Some(made)));
    assert_eq!(answered.path, "/made/bundle.yaml");
    assert_eq!(answered.applied, Some(true));
    assert_eq!(answered.etag, "4");
    assert_eq!(
        answered.bundle.unwrap().name,
        "projects/amber-quiet-heron/bundles/notes"
    );
    let retried = create_bundle::respond(&create_request(), wrote(None));
    assert_eq!(
        retried.path,
        "/work/demo/.ctxloom/content/bundles/v2/notes/bundle.yaml"
    );
}

fn demo() -> library_application::Library {
    Setup {
        bundles: &[("demo", &[ENVELOPE, ("fragments/example.md", "EXAMPLE\n")])],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture()
    .0
}

fn launching() -> Option<v1::Presentable> {
    Some(v1::Presentable {
        can_ask: false,
        can_launch: true,
    })
}

#[test]
fn test_a_fragment_create_with_a_body_stores_it_and_asks_nothing() {
    let (library, recorder) = demo().recording();
    let request = v1::CreateFragmentRequest {
        parent: "projects/amber-quiet-heron/bundles/demo".to_string(),
        fragment_id: "rules".to_string(),
        fragment: Some(v1::Fragment {
            body: "be kind".to_string(),
            ..v1::Fragment::default()
        }),
        caller: Some(v1::Caller {
            presentable: launching(),
            ..v1::Caller::default()
        }),
        ..v1::CreateFragmentRequest::default()
    };
    let asked = super::create_fragment::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(asked.needs.is_empty());
    let planned = recorder.take();
    let library_application::write::recording::Planned::Changed { changes, .. } = &planned[0]
    else {
        panic!("{planned:?}")
    };
    let ctxloom_ports::Change::Write(put) = &changes[0] else {
        panic!()
    };
    assert_eq!(put.path, "fragments/rules.md");
    assert_eq!(put.bytes, b"be kind");
}

// Step 10b: Go's two-word form (`fragment|command create <bundle> <name>`,
// the request's `placeholder`) is served: Go's placeholder written, no
// editor asked; a bundle nobody has is refused, never made.
#[test]
fn test_a_two_word_create_writes_gos_placeholder_and_refuses_a_missing_bundle() {
    // Go's directories: a command's is `prompts/` (command.feature).
    for (kind, dir) in [("fragments", "fragments"), ("commands", "prompts")] {
        let (library, recorder) = demo().recording();
        let parent = "projects/amber-quiet-heron/bundles/demo".to_string();
        let (needs, done) = if kind == "fragments" {
            let request = v1::CreateFragmentRequest {
                parent: parent.clone(),
                fragment_id: "rules".to_string(),
                placeholder: true,
                caller: Some(v1::Caller {
                    presentable: launching(),
                    ..v1::Caller::default()
                }),
                ..v1::CreateFragmentRequest::default()
            };
            let asked = super::create_fragment::plan(&library, &plain(), &request, true)
                .result
                .unwrap();
            (asked.needs, asked.done)
        } else {
            let request = v1::CreateCommandRequest {
                parent: parent.clone(),
                command_id: "rules".to_string(),
                placeholder: true,
                caller: Some(v1::Caller {
                    presentable: launching(),
                    ..v1::Caller::default()
                }),
                ..v1::CreateCommandRequest::default()
            };
            let asked = super::create_command::plan(&library, &plain(), &request, true)
                .result
                .unwrap();
            (asked.needs, asked.done)
        };
        assert!(needs.is_empty(), "no editor: {needs:?}");
        assert!(!done.bundle_created);
        let planned = recorder.take();
        let library_application::write::recording::Planned::Changed { changes, .. } = &planned[0]
        else {
            panic!("{planned:?}")
        };
        let ctxloom_ports::Change::Write(put) = &changes[0] else {
            panic!()
        };
        assert_eq!(put.path, format!("{dir}/rules.md"));
        assert!(
            String::from_utf8_lossy(&put.bytes).contains("Add content here."),
            "{:?}",
            String::from_utf8_lossy(&put.bytes)
        );
    }
    let (library, recorder) = demo().recording();
    let missing = v1::CreateFragmentRequest {
        parent: "projects/amber-quiet-heron/bundles/nobody".to_string(),
        fragment_id: "rules".to_string(),
        placeholder: true,
        ..v1::CreateFragmentRequest::default()
    };
    let refused = super::create_fragment::plan(&library, &plain(), &missing, true)
        .result
        .expect_err("a bundle nobody has is refused");
    assert!(
        angzarr_diagnostic::Diagnostic::code(&refused).contains("bundle-not-found"),
        "{}",
        angzarr_diagnostic::Diagnostic::code(&refused)
    );
    assert!(recorder.take().is_empty());
}

#[test]
fn test_a_fragment_edit_with_no_body_asks_its_edit_need_and_applies_the_answer() {
    let name = "projects/amber-quiet-heron/bundles/demo/fragments/example";
    let preview = v1::UpdateFragmentRequest {
        fragment: Some(v1::Fragment {
            name: name.to_string(),
            ..v1::Fragment::default()
        }),
        caller: Some(v1::Caller {
            presentable: launching(),
            ..v1::Caller::default()
        }),
        ..v1::UpdateFragmentRequest::default()
    };
    let (library, recorder) = demo().recording();
    let asked = super::update_fragment::plan(&library, &plain(), &preview, false)
        .result
        .unwrap();
    assert!(recorder.take().is_empty(), "the preview writes nothing");
    let [need] = &asked.needs[..] else {
        panic!("{:?}", asked.needs)
    };
    assert_eq!(need.id, super::EDIT_NEED);
    let Some(v1::need::Kind::Edit(edit)) = &need.kind else {
        panic!()
    };
    assert_eq!(edit.file_name, "example.md");
    assert_eq!(edit.text, "EXAMPLE\n");
    let launch = edit.launch.as_ref().expect("the editor's launch");
    assert_eq!(launch.kind, v1::launch::Kind::Editor as i32);
    let answered = v1::UpdateFragmentRequest {
        answers: vec![v1::Answer {
            need_id: super::EDIT_NEED.to_string(),
            answer: Some(v1::answer::Answer::Edited("EDITED\n".to_string())),
        }],
        ..preview
    };
    let (library, recorder) = demo().recording();
    let asked = super::update_fragment::plan(&library, &plain(), &answered, true)
        .result
        .unwrap();
    assert!(asked.needs.is_empty());
    assert_eq!(recorder.take().len(), 1);
    let response = super::update_fragment::respond(&answered, wrote(Some(asked)));
    assert_eq!(response.applied, Some(true));
    assert_eq!(response.fragment.unwrap().name, name);
}

#[test]
fn test_an_edit_refreshed_says_its_distilled_form_fresh_and_one_unchanged_applies_nothing() {
    let edited = |status: &str, distilled: bool| super::Asked {
        done: library_application::ItemEdited {
            status: status.to_string(),
            bundle: "demo".to_string(),
            name: "one".to_string(),
            path: "x/one.md".to_string(),
            distilled,
            kind: "command".to_string(),
            reference: String::new(),
            distill_skipped: false,
        },
        needs: Vec::new(),
    };
    let request = v1::UpdateCommandRequest::default();
    let fresh = super::update_command::respond(&request, wrote(Some(edited("updated", true))));
    assert_eq!(
        fresh.command.unwrap().distilled,
        v1::DistilledState::Fresh as i32
    );
    assert_eq!(fresh.applied, Some(true));
    let same = super::update_command::respond(&request, wrote(Some(edited("unchanged", false))));
    assert_eq!(same.applied, Some(false));
    assert_eq!(same.command.unwrap().distilled, 0);
}

#[test]
fn test_a_created_skill_answers_its_directory_done_or_retried() {
    let made = library_application::SkillCreated {
        status: "created".to_string(),
        bundle: "demo".to_string(),
        name: "reviewer".to_string(),
        dir: "skills/reviewer".to_string(),
    };
    let request = v1::CreateSkillRequest {
        parent: "projects/amber-quiet-heron/bundles/demo".to_string(),
        skill_id: "reviewer".to_string(),
        ..v1::CreateSkillRequest::default()
    };
    let answered = super::create_skill::respond(&request, wrote(Some(made)));
    let skill = answered.skill.unwrap();
    assert_eq!(
        skill.name,
        "projects/amber-quiet-heron/bundles/demo/skills/reviewer"
    );
    assert_eq!(skill.path, "skills/reviewer");
    assert_eq!(answered.path, "skills/reviewer");
    let retried = super::create_skill::respond(&request, wrote(None));
    assert_eq!(
        retried.skill.unwrap().name,
        "projects/amber-quiet-heron/bundles/demo/skills/reviewer"
    );
    assert_eq!(retried.path, "/work/demo/.ctxloom/content/bundles/v2/notes");
}

// Step 10b U3: a removal's preview asks its Confirm need, the target in
// the use case's own words; a removal done, or retried once done, is
// applied and asks nothing.
#[test]
fn test_a_removals_preview_asks_its_confirm_need_and_a_removal_done_asks_none() {
    use library_application::{Preview, Removal};
    let preview: Removal<()> = Removal::Preview(Preview::of(
        "bundle \"notes\"".to_string(),
        vec!["1 fragment(s)".to_string()],
        "ctxloom bundle remove notes --yes".to_string(),
    ));
    let removed = super::removed_of(Some(&preview), false);
    assert!(!removed.applied);
    assert_eq!(removed.detail, ["1 fragment(s)"]);
    assert_eq!(removed.apply_command, "ctxloom bundle remove notes --yes");
    let [need] = removed.needs.as_slice() else {
        panic!("{:?}", removed.needs)
    };
    assert_eq!(need.id, super::CONFIRM_NEED);
    let Some(v1::need::Kind::Confirm(confirm)) = &need.kind else {
        panic!("{need:?}")
    };
    assert_eq!(confirm.text, "Remove bundle \"notes\"?");
    assert_eq!(confirm.args[0].name, "target");
    assert_eq!(confirm.args[0].value, "bundle \"notes\"");
    let done = super::removed_of(Some(&Removal::Done(())), false);
    assert!(done.applied && done.needs.is_empty());
    assert!(super::removed_of::<()>(None, true).applied);
    assert!(!super::removed_of::<()>(None, false).applied);
}

// A bundle removal plans its bundle's whole tree gone, the bundle named by
// its resource name, and previews it without planning anything gone.
#[test]
fn test_a_bundle_removal_plans_its_bundle_gone_and_its_preview_nothing() {
    use super::delete_bundle;
    use library_application::write::recording::Planned;
    let request = v1::DeleteBundleRequest {
        name: "projects/amber-quiet-heron/bundles/demo".to_string(),
        ..v1::DeleteBundleRequest::default()
    };
    let (recording, recorder) = demo().recording();
    let previewed = delete_bundle::plan(&recording, &plain(), &request, false)
        .result
        .unwrap();
    assert!(matches!(
        previewed,
        library_application::Removal::Preview(_)
    ));
    assert!(recorder.take().is_empty());
    let (recording, recorder) = demo().recording();
    delete_bundle::plan(&recording, &plain(), &request, true)
        .result
        .unwrap();
    assert_eq!(
        recorder.take(),
        [Planned::BundleGone {
            bundle: "demo".to_string()
        }]
    );
    let answered = delete_bundle::respond(&request, wrote(None));
    assert_eq!(answered.applied, Some(true));
    assert_eq!(
        answered.path,
        "/work/demo/.ctxloom/content/bundles/v2/notes"
    );
    assert_eq!(
        answered.bundle.unwrap().name,
        "projects/amber-quiet-heron/bundles/demo"
    );
}

// Owner ruling 2026-11-19: a bundle removal's preview answers every profile
// and agent that includes the bundle, by resource name, profiles first.
#[test]
fn test_a_bundle_removals_preview_names_its_dependents_by_resource_name() {
    use super::delete_bundle;
    use library_application::{Dependent, DependentKind, Preview, Removal};
    let request = v1::DeleteBundleRequest {
        name: "projects/amber-quiet-heron/bundles/demo".to_string(),
        validate_only: true,
        ..v1::DeleteBundleRequest::default()
    };
    let mut preview = Preview::of(
        "bundle \"demo\"".to_string(),
        Vec::new(),
        "ctxloom bundle remove demo --yes".to_string(),
    );
    preview.dependents = vec![
        Dependent {
            kind: DependentKind::Profile,
            name: "dev".to_string(),
        },
        Dependent {
            kind: DependentKind::Profile,
            name: "other#profiles/direct".to_string(),
        },
        Dependent {
            kind: DependentKind::Agent,
            name: "reviewer".to_string(),
        },
    ];
    let mut previewed = wrote(Some(Removal::Preview(preview)));
    previewed.applied = false;
    let answered = delete_bundle::respond(&request, previewed);
    assert_eq!(
        answered.dependents,
        [
            "projects/amber-quiet-heron/bundles/project/profiles/dev",
            "projects/amber-quiet-heron/bundles/other/profiles/direct",
            "projects/amber-quiet-heron/agents/reviewer",
        ]
    );
    let applied = delete_bundle::respond(&request, wrote(None));
    assert!(applied.dependents.is_empty());
}

// A profile removal answers the profile's canonical name, the project
// bundle's, whatever form it was asked by.
#[test]
fn test_a_profile_removal_answers_the_profiles_canonical_name() {
    let request = v1::DeleteProfileRequest {
        name: "projects/amber-quiet-heron/profiles/dev".to_string(),
        ..v1::DeleteProfileRequest::default()
    };
    let answered = super::delete_profile::respond(&request, wrote(None));
    assert_eq!(
        answered.profile.unwrap().name,
        "projects/amber-quiet-heron/bundles/project/profiles/dev"
    );
    assert_eq!(answered.applied, Some(true));
}

// Step 10b U3 (G4 superseded): an edit's preview with no text to give
// still plans the one file its apply writes, so the daemon answers that
// path's etag for the apply to send back (stale once the file moved);
// it asks its Edit need all the same, and a preview performs nothing.
#[test]
fn test_an_edits_preview_plans_its_file_for_its_etag_and_still_asks_the_edit_need() {
    let preview = v1::UpdateFragmentRequest {
        fragment: Some(v1::Fragment {
            name: "projects/amber-quiet-heron/bundles/demo/fragments/example".to_string(),
            ..v1::Fragment::default()
        }),
        caller: Some(v1::Caller {
            presentable: launching(),
            ..v1::Caller::default()
        }),
        validate_only: true,
        ..v1::UpdateFragmentRequest::default()
    };
    let (library, recorder) = demo().recording();
    let asked = super::update_fragment::plan(&library, &plain(), &preview, true)
        .result
        .unwrap();
    assert_eq!(asked.needs.len(), 1, "{:?}", asked.needs);
    let planned = recorder.take();
    let [library_application::write::recording::Planned::Changed { changes, .. }] = &planned[..]
    else {
        panic!("{planned:?}")
    };
    let [ctxloom_ports::Change::Write(put)] = &changes[..] else {
        panic!("{changes:?}")
    };
    assert_eq!(put.path, "fragments/example.md");
}

fn bundle_edit(
    mask: &[&str],
    edit: impl FnOnce(&mut v1::UpdateBundleRequest),
) -> v1::UpdateBundleRequest {
    let mut request = v1::UpdateBundleRequest {
        bundle: Some(v1::Bundle {
            name: "projects/amber-quiet-heron/bundles/demo".to_string(),
            ..v1::Bundle::default()
        }),
        update_mask: Some(daemon_proto::google::protobuf::FieldMask {
            paths: mask.iter().map(|p| p.to_string()).collect(),
        }),
        ..v1::UpdateBundleRequest::default()
    };
    edit(&mut request);
    request
}

/// The one change a use case planned of a bundle, by its path and text.
fn planned_writes(
    planned: &[library_application::write::recording::Planned],
) -> Vec<(String, String)> {
    planned
        .iter()
        .flat_map(|planned| match planned {
            library_application::write::recording::Planned::Changed { changes, .. } => changes
                .iter()
                .map(|change| match change {
                    ctxloom_ports::Change::Write(put) => (
                        put.path.clone(),
                        String::from_utf8_lossy(&put.bytes).into_owned(),
                    ),
                    other => (format!("{other:?}"), String::new()),
                })
                .collect::<Vec<_>>(),
            other => vec![(format!("{other:?}"), String::new())],
        })
        .collect()
}

#[test]
fn test_a_bundle_edit_writes_the_fields_its_mask_names_and_its_tags_into_the_one_manifest() {
    let (library, recorder) = demo().recording();
    let request = bundle_edit(&["description", "tags"], |r| {
        r.bundle.as_mut().unwrap().description = "updated-desc".to_string();
        r.bundle.as_mut().unwrap().version = "9.9.9".to_string();
        r.add_tags = vec!["golang".to_string()];
    });
    let edited = super::update_bundle::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert_eq!(edited.name, "demo");
    assert_eq!(edited.changes.len(), 2, "{:?}", edited.changes);
    let writes = planned_writes(&recorder.take());
    assert_eq!(writes.len(), 1, "{writes:?}");
    let (path, text) = &writes[0];
    assert_eq!(path, "bundle.yaml");
    assert!(text.contains("updated-desc"), "{text}");
    assert!(text.contains("golang"), "{text}");
    // `version` was given but not named by the mask: not written.
    assert!(!text.contains("9.9.9"), "{text}");
}

#[test]
fn test_a_bundle_edit_adds_and_removes_each_kind_of_item_by_its_list() {
    let (library, recorder) = demo().recording();
    let request = bundle_edit(&["fragments", "commands", "mcp_servers"], |r| {
        r.add_fragments = vec!["a".to_string()];
        r.remove_fragments = vec!["example".to_string()];
        r.add_commands = vec!["b".to_string()];
        r.add_mcp_servers = vec!["tools".to_string()];
    });
    let edited = super::update_bundle::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert_eq!(
        edited.changes,
        [
            "set fragment: a",
            "removed fragment: example",
            "set prompt: b",
            "set mcp: tools"
        ]
    );
    let changes: Vec<String> = match &recorder.take()[0] {
        library_application::write::recording::Planned::Changed { changes, .. } => changes
            .iter()
            .map(|change| match change {
                ctxloom_ports::Change::Write(put) => format!("write {}", put.path),
                ctxloom_ports::Change::Remove { path } => format!("remove {path}"),
                ctxloom_ports::Change::RemoveDir { path } => format!("remove dir {path}"),
            })
            .collect(),
        other => panic!("{other:?}"),
    };
    assert_eq!(
        changes,
        [
            "write fragments/a.md",
            "remove fragments/example.md",
            "write prompts/b.md",
            "write mcp/tools.yaml"
        ]
    );
}

#[test]
fn test_a_bundle_edit_with_no_mask_path_given_its_field_changes_nothing() {
    let (library, recorder) = demo().recording();
    let request = bundle_edit(&[], |r| {
        r.bundle.as_mut().unwrap().description = "updated-desc".to_string();
    });
    let edited = super::update_bundle::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(edited.changes.is_empty(), "{:?}", edited.changes);
    assert!(recorder.take().is_empty());
}

#[test]
fn test_a_bundle_edit_answers_its_changes_and_whether_it_applied() {
    let request = bundle_edit(&["description"], |_| {});
    let edited = |changes: &[&str]| library_application::BundleEdited {
        status: "updated".to_string(),
        name: "demo".to_string(),
        changes: changes.iter().map(|c| c.to_string()).collect(),
        path: "/work/demo/.ctxloom/content/bundles/v2/demo/bundle.yaml".to_string(),
    };
    let answered = super::update_bundle::respond(
        &request,
        wrote(Some(edited(&["description: updated-desc"]))),
    );
    assert_eq!(answered.applied, Some(true));
    assert_eq!(answered.changes, ["description: updated-desc"]);
    assert_eq!(answered.etag, "4");
    assert_eq!(
        answered.path,
        "/work/demo/.ctxloom/content/bundles/v2/demo/bundle.yaml"
    );
    assert_eq!(
        answered.bundle.unwrap().name,
        "projects/amber-quiet-heron/bundles/demo"
    );
    // Nothing to change: nothing applied, and no change listed.
    let unchanged = Wrote {
        applied: false,
        ..wrote(Some(edited(&[])))
    };
    let answered = super::update_bundle::respond(&request, unchanged);
    assert_eq!(answered.applied, Some(false));
    assert!(answered.changes.is_empty());
    // The retry of an edit already applied.
    let retried = super::update_bundle::respond(&request, wrote(None));
    assert_eq!(retried.applied, Some(true));
    assert_eq!(retried.path, "/work/demo/.ctxloom/content/bundles/v2/notes");
}

// Owner ruling 2026-11-18: several mask paths of the one manifest are one
// step; a manifest field beside an item is two files, refused before any
// command, naming both.
#[test]
fn test_a_bundle_edit_of_the_manifest_alone_is_one_step_and_beside_an_item_is_refused() {
    let shaped = |request: &v1::UpdateBundleRequest| {
        let library = demo();
        let (recording, recorder) = library.clone().recording();
        super::update_bundle::plan(&recording, &plain(), request, true)
            .result
            .unwrap();
        crate::writes::shape(recorder.take(), |bundle, dir| {
            library.bundle_files(bundle, dir)
        })
    };
    let manifest = bundle_edit(&["description", "version", "tags"], |r| {
        r.bundle.as_mut().unwrap().description = "updated-desc".to_string();
        r.bundle.as_mut().unwrap().version = "2.0.0".to_string();
        r.add_tags = vec!["golang".to_string()];
    });
    let one = shaped(&manifest).unwrap().unwrap();
    assert_eq!(one.shape.path(), "content/bundles/v2/demo/bundle.yaml");
    let two = bundle_edit(&["version", "fragments"], |r| {
        r.bundle.as_mut().unwrap().version = "2.0.0".to_string();
        r.add_fragments = vec!["testing".to_string()];
    });
    match shaped(&two) {
        Err(library_tree::LibraryTreeRejection::OneChangePerCall { paths }) => {
            assert_eq!(paths, ["bundle.yaml", "fragments/testing.md"]);
        }
        other => panic!("{other:?}"),
    }
}

fn tools() -> library_application::Library {
    Setup {
        bundles: &[(
            "demo",
            &[
                ENVELOPE,
                (
                    "mcp/tools.yaml",
                    "command: echo\nargs:\n  - KEEP-THIS-ARGUMENT\n",
                ),
            ],
        )],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture()
    .0
}

fn mcp_set(mask: &[&str], server: impl FnOnce(&mut v1::McpServer)) -> v1::UpdateMcpServerRequest {
    let mut mcp_server = v1::McpServer {
        name: "projects/amber-quiet-heron/bundles/demo/mcp/tools".to_string(),
        ..v1::McpServer::default()
    };
    server(&mut mcp_server);
    v1::UpdateMcpServerRequest {
        mcp_server: Some(mcp_server),
        update_mask: Some(daemon_proto::google::protobuf::FieldMask {
            paths: mask.iter().map(|p| p.to_string()).collect(),
        }),
        allow_missing: true,
        caller: Some(v1::Caller {
            presentable: launching(),
            ..v1::Caller::default()
        }),
        ..v1::UpdateMcpServerRequest::default()
    }
}

/// The text of the one file a write planned of the bundle.
fn one_write(planned: &[library_application::write::recording::Planned]) -> (String, String) {
    let writes = planned_writes(planned);
    assert_eq!(writes.len(), 1, "{writes:?}");
    writes[0].clone()
}

// An update that may not create (`allow_missing` false) of a server its
// bundle lacks is refused McpNotFound (at the API its proto refusal, as
// `LibraryMcpNotFound`) and plans no write; the CLI's set sends true.
#[test]
fn test_an_mcp_server_update_not_allowed_to_create_refuses_a_missing_one() {
    let (library, recorder) = tools().recording();
    let mut request = mcp_set(&["command"], |s| s.command = "echo".to_string());
    request.allow_missing = false;
    request.mcp_server.as_mut().unwrap().name =
        "projects/amber-quiet-heron/bundles/demo/mcpServers/fresh".to_string();
    let refused = super::update_mcp_server::plan(&library, &plain(), &request, true)
        .result
        .expect_err("a missing server is not created");
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&refused),
        "library-application-mcp-not-found"
    );
    assert!(planned_writes(&recorder.take()).is_empty());
    let mut held = mcp_set(&["command"], |s| s.command = "echo".to_string());
    held.allow_missing = false;
    let written = super::update_mcp_server::plan(&library, &plain(), &held, true)
        .result
        .unwrap();
    assert!(matches!(written, super::McpWritten::Set(_)), "{written:?}");
}

// `mcp server set`: the fields its mask names change, its env among them,
// and the rest keep their value.
#[test]
fn test_an_mcp_server_set_writes_the_fields_its_mask_names_env_included() {
    let (library, recorder) = tools().recording();
    let request = mcp_set(&["command", "env"], |s| {
        s.command = "search-mcp".to_string();
        s.env.insert("LOG_LEVEL".to_string(), "debug".to_string());
        s.url = "https://not.masked".to_string();
    });
    let written = super::update_mcp_server::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    let super::McpWritten::Set(set) = written else {
        panic!("{written:?}")
    };
    assert_eq!((set.bundle.as_str(), set.name.as_str()), ("demo", "tools"));
    assert_eq!(set.status, "updated");
    let (path, text) = one_write(&recorder.take());
    assert_eq!(path, "mcp/tools.yaml");
    assert!(text.contains("command: search-mcp"), "{text}");
    assert!(text.contains("LOG_LEVEL: debug"), "{text}");
    assert!(text.contains("KEEP-THIS-ARGUMENT"), "{text}");
    assert!(!text.contains("not.masked"), "{text}");
}

// A masked list or map given empty clears it; a set of a name the bundle
// lacks creates it.
#[test]
fn test_an_mcp_server_set_clears_a_masked_field_given_empty_and_creates_a_missing_one() {
    let (library, recorder) = tools().recording();
    let request = mcp_set(&["command", "args", "url"], |s| {
        s.url = "https://mcp.example.com/v1".to_string();
    });
    let written = super::update_mcp_server::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(matches!(written, super::McpWritten::Set(_)), "{written:?}");
    let (_, text) = one_write(&recorder.take());
    assert_eq!(text, "url: https://mcp.example.com/v1\n");
    let (library, recorder) = tools().recording();
    let mut request = mcp_set(&["command"], |s| s.command = "echo".to_string());
    request.mcp_server.as_mut().unwrap().name =
        "projects/amber-quiet-heron/bundles/demo/mcpServers/fresh".to_string();
    let super::McpWritten::Set(set) =
        super::update_mcp_server::plan(&library, &plain(), &request, true)
            .result
            .unwrap()
    else {
        panic!()
    };
    assert_eq!(
        (set.name.as_str(), set.status.as_str()),
        ("fresh", "created")
    );
    assert_eq!(one_write(&recorder.take()).0, "mcp/fresh.yaml");
}

// `mcp server edit` (no mask): the server through the editor, the
// preview's Edit need its document, the apply its answer.
#[test]
fn test_an_mcp_server_edit_asks_its_document_and_writes_the_answer() {
    let (library, recorder) = tools().recording();
    let mut preview = mcp_set(&[], |_| {});
    preview.validate_only = true;
    let super::McpWritten::Edited(asked) =
        super::update_mcp_server::plan(&library, &plain(), &preview, false)
            .result
            .unwrap()
    else {
        panic!()
    };
    let Some(v1::need::Kind::Edit(edit)) = &asked.needs[0].kind else {
        panic!("{:?}", asked.needs)
    };
    assert!(edit.text.contains("KEEP-THIS-ARGUMENT"), "{}", edit.text);
    assert_eq!(edit.file_name, "tools.yaml");
    let _ = recorder.take();
    let mut apply = mcp_set(&[], |_| {});
    apply.answers = vec![v1::Answer {
        need_id: super::EDIT_NEED.to_string(),
        answer: Some(v1::answer::Answer::Edited(
            "command: EDITED-BY-TEST\nargs:\n  - KEEP-THIS-ARGUMENT\n".to_string(),
        )),
    }];
    let super::McpWritten::Edited(asked) =
        super::update_mcp_server::plan(&library, &plain(), &apply, true)
            .result
            .unwrap()
    else {
        panic!()
    };
    assert!(asked.needs.is_empty());
    assert_eq!(
        asked.done,
        library_application::McpEdited::Updated {
            name: "tools".to_string(),
            bundle: "demo".to_string()
        }
    );
    let (path, text) = one_write(&recorder.take());
    assert_eq!(path, "mcp/tools.yaml");
    assert!(text.contains("EDITED-BY-TEST"), "{text}");
}

#[test]
fn test_an_mcp_server_write_answers_its_server_whether_created_and_applied() {
    let request = mcp_set(&["command"], |_| {});
    let set = library_application::McpSet {
        status: "created".to_string(),
        bundle: "demo".to_string(),
        name: "tools".to_string(),
        path: "/work/demo/.ctxloom/content/bundles/v2/demo/bundle.yaml".to_string(),
    };
    let answered =
        super::update_mcp_server::respond(&request, wrote(Some(super::McpWritten::Set(set))));
    assert_eq!(
        answered.mcp_server.unwrap().name,
        "projects/amber-quiet-heron/bundles/demo/mcpServers/tools"
    );
    assert!(answered.created);
    assert_eq!(answered.applied, Some(true));
    assert_eq!(answered.etag, "4");
    // Applied by the call, yet the editor changed nothing: nothing applied.
    let unchanged = Wrote {
        ..wrote(Some(super::McpWritten::Edited(super::Asked {
            done: library_application::McpEdited::Unchanged,
            needs: Vec::new(),
        })))
    };
    let answered = super::update_mcp_server::respond(&request, unchanged);
    assert_eq!(answered.applied, Some(false));
    assert!(!answered.created);
    let retried = super::update_mcp_server::respond(&request, wrote(None));
    assert_eq!(retried.applied, Some(true));
    assert_eq!(
        retried.mcp_server.unwrap().name,
        "projects/amber-quiet-heron/bundles/demo/mcp/tools"
    );
}

const AGENTS: &str = "schema_version: 9\ndefault_agent: dev\nagents:\n  dev:\n    engine: claude-code\n    profiles: [default]\n    runtime: host\n    engines:\n      claude-code:\n        model: haiku\n";

fn agents() -> library_application::Library {
    Setup {
        bundles: &[("demo", &[ENVELOPE])],
        layers: &[AGENTS],
        ..Setup::default()
    }
    .fixture()
    .0
}

/// The one config layer a write planned, and its scope.
fn one_layer(
    planned: &[library_application::write::recording::Planned],
) -> (ctxloom_ports::Scope, String) {
    use library_application::write::recording::Planned;
    let layers: Vec<_> = planned
        .iter()
        .filter_map(|p| match p {
            Planned::Layer { scope, text } => Some((*scope, text.clone())),
            _ => None,
        })
        .collect();
    assert_eq!(layers.len(), 1, "{planned:?}");
    layers[0].clone()
}

fn agent_update(mask: &[&str], agent: impl FnOnce(&mut v1::Agent)) -> v1::UpdateAgentRequest {
    let mut named = v1::Agent {
        name: "projects/amber-quiet-heron/agents/dev".to_string(),
        ..<v1::Agent as Default>::default()
    };
    agent(&mut named);
    v1::UpdateAgentRequest {
        agent: Some(named),
        update_mask: Some(daemon_proto::google::protobuf::FieldMask {
            paths: mask.iter().map(|p| p.to_string()).collect(),
        }),
        ..v1::UpdateAgentRequest::default()
    }
}

// `agent create`: the binding its fields give (model and effort into its
// engine's block, its surfaces and roots, its env), into the home config
// when the request says `home`; answered with the agent written.
#[test]
fn test_an_agent_create_writes_the_binding_its_fields_give_where_home_says() {
    let (library, recorder) = agents().recording();
    let request = v1::CreateAgentRequest {
        parent: "projects/amber-quiet-heron".to_string(),
        agent_id: "reviewer".to_string(),
        agent: Some(v1::Agent {
            engine: "claude-code".to_string(),
            profiles: vec!["default".to_string()],
            model: "sonnet".to_string(),
            effort: "low".to_string(),
            surfaces: [("context".to_string(), "system-prompt".to_string())].into(),
            env: vec!["ALPHA_TOKEN".to_string()],
            ..<v1::Agent as Default>::default()
        }),
        ..v1::CreateAgentRequest::default()
    };
    let made = super::create_agent::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    let library_application::AgentWritten::Done(row) = &made.written else {
        panic!("{made:?}")
    };
    assert_eq!(row.name, "reviewer");
    assert_eq!(made.default, "dev", "the agent a bare run binds");
    let (scope, text) = one_layer(&recorder.take());
    assert_eq!(scope, ctxloom_ports::Scope::Project);
    assert!(text.contains("reviewer:"), "{text}");
    assert!(
        text.contains("model: sonnet") && text.contains("effort: low"),
        "{text}"
    );
    assert!(text.contains("context: system-prompt"), "{text}");
    assert!(text.contains("ALPHA_TOKEN"), "{text}");
    let home = v1::CreateAgentRequest {
        home: true,
        ..request
    };
    super::create_agent::plan(&library, &plain(), &home, true)
        .result
        .unwrap();
    assert_eq!(one_layer(&recorder.take()).0, ctxloom_ports::Scope::Home);
}

// `agent edit`: only what its mask names changes (a field named and given
// empty is cleared); a model goes to the agent's engine's block.
#[test]
fn test_an_agent_edit_writes_the_fields_its_mask_names_alone() {
    let (library, recorder) = agents().recording();
    let request = agent_update(&["runtime", "effort"], |a| {
        a.runtime = "container-rootless".to_string();
        a.effort = "high".to_string();
        a.engine = "opencode".to_string();
        a.profiles = vec!["not-masked".to_string()];
    });
    let edited = super::update_agent::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(matches!(
        edited.written,
        library_application::AgentWritten::Done(_)
    ));
    let (_, text) = one_layer(&recorder.take());
    assert!(text.contains("runtime: container-rootless"), "{text}");
    assert!(
        text.contains("effort: high") && text.contains("model: haiku"),
        "{text}"
    );
    assert!(text.contains("engine: claude-code"), "{text}");
    assert!(
        !text.contains("not-masked") && !text.contains("opencode"),
        "{text}"
    );
    let cleared = agent_update(&["model"], |_| {});
    super::update_agent::plan(&library, &plain(), &cleared, true)
        .result
        .unwrap();
    let (_, text) = one_layer(&recorder.take());
    assert!(!text.contains("haiku"), "{text}");
}

// An edit answers the agent as written with whether it is the default; a
// swap's preview answers its engines and what is given up, and the command
// that applies it, applied false.
#[test]
fn test_an_agent_write_answers_the_agent_or_the_swaps_preview() {
    use library_application::{AgentRow, AgentWritten, SwapPreview};
    let request = agent_update(&["engine"], |a| a.engine = "opencode".to_string());
    let row = AgentRow {
        name: "dev".to_string(),
        engine: "opencode".to_string(),
        profiles: Vec::new(),
        runtime: String::new(),
        driving: String::new(),
        env: Vec::new(),
        session_home: String::new(),
        engines: serde_json::Value::Null,
        engine_lines: Vec::new(),
    };
    let done = super::update_agent::respond(
        &request,
        wrote(Some(super::AgentWrote {
            written: AgentWritten::Done(row),
            default: "dev".to_string(),
        })),
    );
    let agent = done.agent.unwrap();
    assert_eq!(agent.name, "projects/amber-quiet-heron/agents/dev");
    assert_eq!(agent.engine, "projects/amber-quiet-heron/engines/opencode");
    assert_eq!(agent.default, Some(true));
    assert_eq!(done.applied, Some(true));
    assert_eq!(done.etag, "4");
    let preview = SwapPreview {
        status: "preview".to_string(),
        applied: false,
        agent: "dev".to_string(),
        from: "claude-code".to_string(),
        to: "opencode".to_string(),
        not_carried: Vec::new(),
        apply: "ctxloom agent edit dev --engine opencode --yes".to_string(),
    };
    let mut previewed = wrote(Some(super::AgentWrote {
        written: AgentWritten::Preview(preview),
        default: String::new(),
    }));
    previewed.applied = false;
    let swap = super::update_agent::respond(&request, previewed);
    assert_eq!(swap.applied, Some(false));
    assert_eq!(
        (swap.from.as_str(), swap.to.as_str()),
        ("claude-code", "opencode")
    );
    assert_eq!(
        swap.apply_command,
        "ctxloom agent edit dev --engine opencode --yes"
    );
    assert_eq!(
        swap.agent.unwrap().name,
        "projects/amber-quiet-heron/agents/dev"
    );
    // Step 10b, owner ruling 2026-11-18 (evening): the swap's preview asks
    // a Confirm need, as a removal's does; an applied edit asks none.
    let arg = |name: &str, value: &str| v1::NeedArg {
        name: name.to_string(),
        value: value.to_string(),
    };
    assert_eq!(
        swap.needs,
        [v1::Need {
            optional: false,
            id: super::CONFIRM_NEED.to_string(),
            kind: Some(v1::need::Kind::Confirm(v1::Confirm {
                text: "Apply the move of agent \"dev\" from claude-code to opencode, \
                       giving up what opencode cannot carry?"
                    .to_string(),
                code: String::new(),
                args: vec![
                    arg("agent", "dev"),
                    arg("from", "claude-code"),
                    arg("to", "opencode"),
                ],
            })),
        }]
    );
    assert!(done.needs.is_empty());
}

fn config_edit(answers: Vec<v1::Answer>, validate_only: bool) -> v1::EditConfigRequest {
    v1::EditConfigRequest {
        name: "projects/amber-quiet-heron/config".to_string(),
        validate_only,
        answers,
        caller: Some(v1::Caller {
            presentable: launching(),
            ..v1::Caller::default()
        }),
        ..v1::EditConfigRequest::default()
    }
}

// `config edit`: its preview asks the config's text as an Edit need (and
// plans the one layer, for its etag); its apply writes the need's answer
// whole; an answer the same as the text applies nothing.
#[test]
fn test_a_config_edit_asks_its_text_and_writes_the_answer_whole() {
    let (library, recorder) = agents().recording();
    let preview =
        super::edit_config::plan(&library, &plain(), &config_edit(Vec::new(), true), false)
            .result
            .unwrap();
    assert_eq!(preview.needs.len(), 1, "{preview:?}");
    let Some(v1::need::Kind::Edit(edit)) = &preview.needs[0].kind else {
        panic!("{preview:?}")
    };
    assert_eq!(edit.text, AGENTS);
    assert_eq!(one_layer(&recorder.take()).0, ctxloom_ports::Scope::Project);
    let answer = v1::Answer {
        need_id: super::EDIT_NEED.to_string(),
        answer: Some(v1::answer::Answer::Edited(
            "schema_version: 9\n".to_string(),
        )),
    };
    let applied =
        super::edit_config::plan(&library, &plain(), &config_edit(vec![answer], false), true)
            .result
            .unwrap();
    assert!(applied.done.changed && applied.needs.is_empty());
    assert_eq!(one_layer(&recorder.take()).1, "schema_version: 9\n");
    let same = v1::Answer {
        need_id: super::EDIT_NEED.to_string(),
        answer: Some(v1::answer::Answer::Edited(AGENTS.to_string())),
    };
    let unchanged =
        super::edit_config::plan(&library, &plain(), &config_edit(vec![same], false), true)
            .result
            .unwrap();
    assert!(!unchanged.done.changed);
    assert!(recorder.take().is_empty());
    let answered =
        super::edit_config::respond(&config_edit(Vec::new(), false), wrote(Some(unchanged)));
    assert_eq!(answered.applied, Some(false), "left as it was");
    assert_eq!(
        answered.config.unwrap().name,
        "projects/amber-quiet-heron/config"
    );
}

fn import_request(source: &str, force: bool) -> v1::ImportBundleRequest {
    v1::ImportBundleRequest {
        parent: "projects/amber-quiet-heron".to_string(),
        source: source.to_string(),
        force,
        caller: Some(v1::Caller {
            cwd: "/work/demo".to_string(),
            ..v1::Caller::default()
        }),
        ..v1::ImportBundleRequest::default()
    }
}

// Step 10b U9: an import's source is the caller's path, so one given
// relative is read from her working directory, never the daemon's; a
// forced one over a bundle is planned as it gone, then the new one (T28).
#[test]
fn test_a_bundle_import_reads_its_source_from_the_callers_directory() {
    use super::import_bundle;
    use library_application::write::recording::Planned;
    let (library, mem) = Setup {
        bundles: &[("notes", &[ENVELOPE])],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture();
    mem.put_user(
        "/work/demo/exported/notes/bundle.yaml",
        ENVELOPE.1.as_bytes(),
        false,
    );
    let (recording, recorder) = library.clone().recording();
    let imported = import_bundle::plan(
        &recording,
        &plain(),
        &import_request("exported/notes", true),
        true,
    )
    .result
    .unwrap();
    assert_eq!(imported.source, "/work/demo/exported/notes");
    let planned = recorder.take();
    assert!(
        matches!(
            &planned[..],
            [Planned::BundleGone { .. }, Planned::NewBundle { .. }]
        ),
        "{planned:?}"
    );
    // Absolute, it is read where it says.
    mem.put_user("/elsewhere/notes/bundle.yaml", ENVELOPE.1.as_bytes(), false);
    let (recording, _) = library.clone().recording();
    let imported = import_bundle::plan(
        &recording,
        &plain(),
        &import_request("/elsewhere/notes", true),
        true,
    )
    .result
    .unwrap();
    assert_eq!(imported.source, "/elsewhere/notes");
    // Not forced, a bundle there refuses it.
    let (recording, _) = library.clone().recording();
    assert!(import_bundle::plan(
        &recording,
        &plain(),
        &import_request("exported/notes", false),
        true
    )
    .result
    .is_err());
}

#[test]
fn test_a_bundle_import_answers_its_bundle_counts_and_path_done_or_retried() {
    use super::import_bundle;
    let done = library_application::BundleImported {
        status: "imported".to_string(),
        source: "/work/demo/exported/notes".to_string(),
        dest: "/work/demo/.ctxloom/content/bundles/v2/notes".to_string(),
        version: "1.2.0".to_string(),
        fragments: 2,
        commands: 1,
        mcp: 0,
    };
    let answered =
        import_bundle::respond(&import_request("exported/notes", true), wrote(Some(done)));
    let bundle = answered.bundle.unwrap();
    assert_eq!(bundle.name, "projects/amber-quiet-heron/bundles/notes");
    assert_eq!(bundle.version, "1.2.0");
    assert_eq!((bundle.fragment_count, bundle.command_count), (2, 1));
    assert_eq!(
        answered.path,
        "/work/demo/.ctxloom/content/bundles/v2/notes"
    );
    assert_eq!(
        (answered.applied, answered.etag.as_str()),
        (Some(true), "4")
    );
    // A retry of one applied answers its place and its bundle's name.
    let retried = import_bundle::respond(&import_request("exported/notes", true), wrote(None));
    assert_eq!(retried.path, "/work/demo/.ctxloom/content/bundles/v2/notes");
    assert_eq!(
        retried.bundle.unwrap().name,
        "projects/amber-quiet-heron/bundles/notes"
    );
}

fn profiled() -> library_application::Library {
    Setup {
        bundles: &[
            ("demo", &[ENVELOPE, ("fragments/example.md", "EXAMPLE\n")]),
            ("ops", &[ENVELOPE]),
            (
                "project",
                &[ENVELOPE, ("profiles/dev.yaml", "bundles:\n  - demo\n")],
            ),
        ],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture()
    .0
}

fn profile_create(parent: &str, profile: v1::Profile) -> v1::CreateProfileRequest {
    v1::CreateProfileRequest {
        parent: parent.to_string(),
        profile_id: "clinic".to_string(),
        profile: Some(profile),
        ..v1::CreateProfileRequest::default()
    }
}

// Step 10b U3: `profile create` is served; a profile into the project's own
// bundle, which no one made yet, is the bundle made with it (one new
// directory, its manifest and the profile), its skills curated as given.
#[test]
fn test_a_profile_create_makes_the_project_bundle_with_it_its_skills_given() {
    let (library, recorder) = demo().recording();
    let request = profile_create(
        "projects/amber-quiet-heron",
        v1::Profile {
            description: "day-to-day".to_string(),
            bundles: vec!["demo".to_string()],
            skills: vec!["demo#skills/doctor".to_string()],
            ..<v1::Profile as Default>::default()
        },
    );
    let made = super::create_profile::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert_eq!(made.name, "clinic");
    let planned = recorder.take();
    let [library_application::write::recording::Planned::NewBundle { bundle, files }] =
        &planned[..]
    else {
        panic!("{planned:?}")
    };
    assert_eq!(bundle, "project");
    let paths: Vec<&str> = files.iter().map(|f| f.path.as_str()).collect();
    assert_eq!(paths, ["bundle.yaml", "profiles/clinic.yaml"]);
    let text = String::from_utf8_lossy(&files[1].bytes);
    assert!(text.contains("demo#skills/doctor"), "{text}");
    assert!(text.contains("day-to-day"), "{text}");
}

// The bundle its parent names (`--bundle`) is the one written into.
#[test]
fn test_a_profile_create_under_a_bundle_writes_into_that_bundle() {
    let (library, recorder) = profiled().recording();
    let request = profile_create(
        "projects/amber-quiet-heron/bundles/ops",
        v1::Profile {
            parents: vec!["dev".to_string()],
            ..<v1::Profile as Default>::default()
        },
    );
    super::create_profile::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    let planned = recorder.take();
    let [library_application::write::recording::Planned::Changed { bundle, changes }] =
        &planned[..]
    else {
        panic!("{planned:?}")
    };
    assert_eq!(bundle, "ops");
    let [ctxloom_ports::Change::Write(put)] = &changes[..] else {
        panic!("{changes:?}")
    };
    assert_eq!(put.path, "profiles/clinic.yaml");
}

#[test]
fn test_a_profile_create_answers_the_profile_named_in_its_bundle_done_or_retried() {
    let request = profile_create(
        "projects/amber-quiet-heron",
        v1::Profile {
            bundles: vec!["demo".to_string()],
            ..<v1::Profile as Default>::default()
        },
    );
    let made = library_application::ProfileCreated {
        name: "clinic".to_string(),
        parents: Vec::new(),
        bundles: vec!["demo".to_string()],
        path: ".ctxloom/content/bundles/v2/project/profiles/clinic.yaml".to_string(),
    };
    let answered = super::create_profile::respond(&request, wrote(Some(made)));
    let profile = answered.profile.unwrap();
    assert_eq!(
        profile.name,
        "projects/amber-quiet-heron/bundles/project/profiles/clinic"
    );
    assert_eq!(profile.bundles, ["projects/amber-quiet-heron/bundles/demo"]);
    assert_eq!(
        answered.path,
        ".ctxloom/content/bundles/v2/project/profiles/clinic.yaml"
    );
    assert_eq!(answered.applied, Some(true));
    assert_eq!(answered.etag, "4");
    // A retry of a create already applied: named alone, still applied.
    let retried = super::create_profile::respond(&request, wrote(None));
    assert_eq!(
        retried.profile.unwrap().name,
        "projects/amber-quiet-heron/bundles/project/profiles/clinic"
    );
    assert_eq!(retried.applied, Some(true));
}

fn profile_update(
    mask: &[&str],
    edit: impl FnOnce(&mut v1::UpdateProfileRequest),
) -> v1::UpdateProfileRequest {
    let mut request = v1::UpdateProfileRequest {
        profile: Some(v1::Profile {
            name: "projects/amber-quiet-heron/profiles/dev".to_string(),
            ..<v1::Profile as Default>::default()
        }),
        update_mask: (!mask.is_empty()).then(|| daemon_proto::google::protobuf::FieldMask {
            paths: mask.iter().map(|p| p.to_string()).collect(),
        }),
        caller: Some(v1::Caller {
            presentable: launching(),
            ..v1::Caller::default()
        }),
        ..v1::UpdateProfileRequest::default()
    };
    edit(&mut request);
    request
}

// A patch (`profile edit --add-bundle ...`) is the profile's one file
// changed by its lists and the fields its mask names; it asks nothing.
#[test]
fn test_a_profile_patch_writes_its_lists_and_masked_fields_into_its_one_file() {
    let (library, recorder) = profiled().recording();
    let request = profile_update(&["description"], |r| {
        r.profile.as_mut().unwrap().description = "patched".to_string();
        r.add_bundles = vec!["ops".to_string()];
        r.exclude_fragments = vec!["example".to_string()];
    });
    let asked = super::update_profile::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(asked.needs.is_empty(), "{:?}", asked.needs);
    let writes = planned_writes(&recorder.take());
    let [(path, text)] = &writes[..] else {
        panic!("{writes:?}")
    };
    assert_eq!(path, "profiles/dev.yaml");
    assert!(text.contains("patched"), "{text}");
    assert!(text.contains("ops"), "{text}");
    assert!(text.contains("example"), "{text}");
    let answered = super::update_profile::respond(&request, wrote(Some(asked)));
    assert_eq!(answered.applied, Some(true));
    assert!(!answered.changes.is_empty());
    assert_eq!(
        answered.profile.unwrap().name,
        "projects/amber-quiet-heron/bundles/project/profiles/dev"
    );
}

// A description given but not named by the mask is not written: nothing
// to change, nothing applied.
#[test]
fn test_a_profile_patch_with_no_mask_path_for_its_field_changes_nothing() {
    let (library, recorder) = profiled().recording();
    let request = profile_update(&[], |r| {
        r.profile.as_mut().unwrap().description = "patched".to_string();
        r.add_parents = vec![];
        r.remove_bundles = vec!["no-such".to_string()];
    });
    let asked = super::update_profile::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(asked.needs.is_empty());
    assert!(recorder.take().is_empty());
    let answered = super::update_profile::respond(&request, wrote(Some(asked)));
    assert_eq!(answered.applied, Some(false));
}

// With no mask path and no list, `profile edit` is the editor's round
// trip: the preview asks its Edit need (the profile's text), the apply
// writes the answer.
#[test]
fn test_a_profile_edit_with_nothing_given_asks_its_document_and_writes_the_answer() {
    let preview = profile_update(&[], |r| r.validate_only = true);
    let (library, recorder) = profiled().recording();
    let asked = super::update_profile::plan(&library, &plain(), &preview, false)
        .result
        .unwrap();
    let [need] = &asked.needs[..] else {
        panic!("{:?}", asked.needs)
    };
    let Some(v1::need::Kind::Edit(edit)) = &need.kind else {
        panic!()
    };
    assert_eq!(edit.file_name, "dev.yaml");
    assert_eq!(edit.text, "bundles:\n  - demo\n");
    recorder.take();
    let answered = v1::UpdateProfileRequest {
        validate_only: false,
        answers: vec![v1::Answer {
            need_id: super::EDIT_NEED.to_string(),
            answer: Some(v1::answer::Answer::Edited(
                "bundles:\n  - ops\n".to_string(),
            )),
        }],
        ..preview
    };
    let (library, recorder) = profiled().recording();
    let asked = super::update_profile::plan(&library, &plain(), &answered, true)
        .result
        .unwrap();
    assert!(asked.needs.is_empty());
    let writes = planned_writes(&recorder.take());
    assert_eq!(
        writes,
        [(
            "profiles/dev.yaml".to_string(),
            "bundles:\n  - ops\n".to_string()
        )]
    );
    let response = super::update_profile::respond(&answered, wrote(Some(asked)));
    assert_eq!(response.applied, Some(true));
    assert!(
        response.path.ends_with("profiles/dev.yaml"),
        "{}",
        response.path
    );
}

// `profile modify <name>` alone sends a mask naming nothing: a patch that
// changes nothing, never the editor.
#[test]
fn test_a_profile_patch_of_an_empty_mask_asks_no_editor_and_changes_nothing() {
    let (library, recorder) = profiled().recording();
    let request = profile_update(&[], |r| {
        r.update_mask = Some(daemon_proto::google::protobuf::FieldMask::default());
    });
    let asked = super::update_profile::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert!(asked.needs.is_empty(), "{:?}", asked.needs);
    assert!(recorder.take().is_empty());
    assert_eq!(
        super::update_profile::respond(&request, wrote(Some(asked))).applied,
        Some(false)
    );
}

// Step 10b (D2's EXCEPTION): an export plans the bundle's tree outside the
// trees, at her path below her working directory, and writes nothing; its
// answer says the destination as she typed it.
#[test]
fn test_a_bundle_export_plans_its_tree_below_the_callers_directory_and_answers_it_as_typed() {
    let (library, recorder) = demo().recording();
    let request = v1::ExportBundleRequest {
        name: "projects/amber-quiet-heron/bundles/demo".to_string(),
        destination: "exported".to_string(),
        caller: Some(v1::Caller {
            cwd: "/work".to_string(),
            ..v1::Caller::default()
        }),
        ..v1::ExportBundleRequest::default()
    };
    let done = super::export_bundle::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    let planned = recorder.take();
    let [library_application::write::recording::Planned::Outside { dir, files }] = &planned[..]
    else {
        panic!("{planned:?}")
    };
    assert_eq!(dir, "/work/exported/demo");
    assert!(files.iter().any(|f| f.path == "fragments/example.md"));
    let answered = super::export_bundle::respond(&request, wrote(Some(done)));
    assert_eq!(answered.destination, "exported/demo");
    assert_eq!(answered.applied, Some(true));
    assert_eq!(answered.bundle, "projects/amber-quiet-heron/bundles/demo");
    // AIP-155: the retry of an export made before is answered with what it
    // answered then (its source and where it went), applied.
    let retried = super::export_bundle::respond(
        &request,
        Wrote {
            at: "/work/exported/demo".to_string(),
            source: "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml".to_string(),
            ..wrote(None)
        },
    );
    assert_eq!(retried.applied, Some(true));
    assert_eq!(retried.destination, "exported/demo");
    assert_eq!(
        retried.source,
        "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml"
    );
    let output = v1::ExportBundleRequest {
        destination: String::new(),
        output: "handoff".to_string(),
        ..request
    };
    let (library, recorder) = demo().recording();
    super::export_bundle::plan(&library, &plain(), &output, true)
        .result
        .unwrap();
    let planned = recorder.take();
    let [library_application::write::recording::Planned::Outside { dir, .. }] = &planned[..] else {
        panic!("{planned:?}")
    };
    assert_eq!(dir, "/work/handoff");
}

// Step 10b U9 (QUESTIONS 1(a)): a local move plans the bundle's one move
// into the project at her directory, below her working directory, and
// answers where it was and where it landed, as she typed it; a retry
// (no answer) says where it landed by the layout.
#[test]
fn test_a_bundle_move_plans_its_one_move_and_answers_where_it_landed_as_typed() {
    let (library, recorder) = demo().recording();
    let request = v1::MoveBundleRequest {
        name: "projects/amber-quiet-heron/bundles/demo".to_string(),
        destination: "other".to_string(),
        caller: Some(v1::Caller {
            cwd: "/work".to_string(),
            ..v1::Caller::default()
        }),
        ..v1::MoveBundleRequest::default()
    };
    let done = super::move_bundle::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    assert_eq!(
        recorder.take(),
        [library_application::write::recording::Planned::MovedInto {
            bundle: "demo".to_string(),
            app_dir: "/work/other/.ctxloom".to_string(),
        }]
    );
    let answered = super::move_bundle::respond(&request, wrote(Some(done)));
    assert_eq!(answered.path, "other/.ctxloom/content/bundles/v2/demo");
    assert!(
        answered.source.ends_with("demo/bundle.yaml"),
        "{}",
        answered.source
    );
    assert_eq!(answered.applied, Some(true));
    assert!(answered.bundle.is_none());
    let retried = super::move_bundle::respond(&request, wrote(None));
    assert_eq!(retried.path, "other/.ctxloom/content/bundles/v2/demo");
}

/// A home layer (the fixture's first) beside the project's.
const HOME_LAYER: &str = "schema_version: 9\neditor:\n  command: 'true'\n";

fn companioned() -> (
    library_application::Library,
    std::sync::Arc<library_application::fakes::Mem>,
) {
    let (library, mem) = Setup {
        layers: &[HOME_LAYER, CONFIG],
        ..Setup::default()
    }
    .fixture();
    mem.set_companion(
        "acme",
        ctxloom_ports::CompanionLoadout::Loaded("run:\n  version: 1.0.0\n".to_string()),
    );
    (library, mem)
}

// Step 10b U3: `companion add` is served; it probes the program the name
// stands for and registers the name in the HOME config, one layer; its
// answer is the companion, applied only when it was new.
#[test]
fn test_a_companion_add_registers_it_in_the_home_layer_and_answers_it() {
    let (library, _) = companioned();
    let (recording, recorder) = library.recording();
    let request = v1::CreateCompanionRequest {
        parent: "projects/amber-quiet-heron".to_string(),
        companion_id: "acme".to_string(),
        ..v1::CreateCompanionRequest::default()
    };
    let done = super::create_companion::plan(&recording, &plain(), &request, true)
        .result
        .unwrap();
    let (scope, text) = one_layer(&recorder.take());
    assert_eq!(scope, ctxloom_ports::Scope::Home);
    assert!(text.contains("acme"), "{text}");
    let answered = super::create_companion::respond(&request, wrote(Some(done.clone())));
    let companion = answered.companion.unwrap();
    assert_eq!(companion.name, "projects/amber-quiet-heron/companions/acme");
    assert_eq!(companion.bin, "ctxloom-companion-acme");
    assert_eq!(answered.applied, Some(true));
    let already = library_application::CompanionAdded {
        added: false,
        ..done
    };
    let mut unchanged = wrote(Some(already));
    unchanged.applied = false;
    assert_eq!(
        super::create_companion::respond(&request, unchanged).applied,
        Some(false)
    );
}

// Step 10b (owner 2026-11-18, night): `companion disable|enable` are
// served writes of the HOME config, one layer; each answers the companion
// as the write leaves it, applied only when its state changed.
#[test]
fn test_a_companion_disable_and_enable_write_the_home_layer_and_answer_it() {
    let (library, _) = companioned();
    library
        .companion_add(&plain(), "acme")
        .result
        .expect("acme registers");
    let name = "projects/amber-quiet-heron/companions/acme".to_string();
    let (recording, recorder) = library.recording();
    let disable = v1::DisableCompanionRequest {
        name: name.clone(),
        ..v1::DisableCompanionRequest::default()
    };
    let done = super::disable_companion::plan(&recording, &plain(), &disable, true)
        .result
        .unwrap();
    let (scope, text) = one_layer(&recorder.take());
    assert_eq!(scope, ctxloom_ports::Scope::Home);
    assert!(text.contains("disabled"), "{text}");
    let answered = super::disable_companion::respond(&disable, wrote(Some(done.clone())));
    let companion = answered.companion.unwrap();
    assert_eq!(companion.name, name);
    assert_eq!(companion.bin, "ctxloom-companion-acme");
    assert_eq!(companion.state, v1::companion::State::Disabled as i32);
    assert_eq!(answered.applied, Some(true));
    assert_eq!(answered.etag, "4");
    // Already in that state: nothing applied.
    let same = library_application::CompanionStateSet {
        changed: false,
        ..done
    };
    assert_eq!(
        super::disable_companion::respond(&disable, wrote(Some(same))).applied,
        Some(false)
    );
    let enable = v1::EnableCompanionRequest {
        name: name.clone(),
        ..v1::EnableCompanionRequest::default()
    };
    let (library, _) = companioned();
    library.companion_add(&plain(), "acme").result.unwrap();
    library
        .companion_set_state(
            &plain(),
            "acme",
            library_application::CompanionState::Disabled,
        )
        .result
        .unwrap();
    let (recording, recorder) = library.recording();
    let done = super::enable_companion::plan(&recording, &plain(), &enable, true)
        .result
        .unwrap();
    assert_eq!(one_layer(&recorder.take()).0, ctxloom_ports::Scope::Home);
    let answered = super::enable_companion::respond(&enable, wrote(Some(done)));
    assert_eq!(
        answered.companion.unwrap().state,
        v1::companion::State::Enabled as i32
    );
    // A retry of a write already applied: applied, its companion named.
    let retried = super::enable_companion::respond(&enable, wrote(None));
    assert_eq!(retried.applied, Some(true));
    assert_eq!(retried.companion.unwrap().name, name);
}

// Step 10b U3: `agent default <name>` is served: the default written to
// the project's config, one layer; its answer names the agent and whether
// it is defined yet.
#[test]
fn test_an_agent_default_set_writes_one_layer_and_says_whether_it_is_defined() {
    let (library, recorder) = agents().recording();
    let request = v1::SetDefaultAgentRequest {
        parent: "projects/amber-quiet-heron".to_string(),
        agent: "nobody-yet".to_string(),
        ..v1::SetDefaultAgentRequest::default()
    };
    let done = super::set_default_agent::plan(&library, &plain(), &request, true)
        .result
        .unwrap();
    let (_, text) = one_layer(&recorder.take());
    assert!(text.contains("nobody-yet"), "{text}");
    let answered = super::set_default_agent::respond(&request, wrote(Some(done)));
    assert_eq!(answered.agent, "nobody-yet");
    assert!(!answered.defined);
    assert_eq!(answered.applied, Some(true));
}
