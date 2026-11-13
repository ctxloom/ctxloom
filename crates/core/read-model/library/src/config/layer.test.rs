//! Ported from Go's `configload` tests (`layering_test.go`, `merge_test.go`,
//! `config_version_refusal_test.go`), `internal/migrations/config/internal/v8`
//! and `confload` (`TestLoad_WarnsWhenAConfigFileExistsButDefinesNoKeys`) at
//! 860336c4e.

use super::*;
use crate::config::{CURRENT, OLDEST};

angzarr_presentation::english_for!(&[crate::EN_US.1]);

fn parse(text: &str) -> Value {
    angzarr_yaml::parse(text).unwrap_or_else(|e| panic!("{e}"))
}

fn merged(layers: &[&str]) -> Value {
    let layers: Vec<Value> = layers.iter().map(|l| parse(l)).collect();
    merge_layers(&layers)
}

fn get<'a>(v: &'a Value, path: &str) -> Option<&'a Value> {
    path.split('.').try_fold(v, |here, key| here.get(key))
}

fn text<'a>(v: &'a Value, path: &str) -> &'a str {
    get(v, path)
        .and_then(Value::as_str)
        .unwrap_or_else(|| panic!("no {path} in {v:?}"))
}

fn seq(v: &Value, path: &str) -> Vec<String> {
    get(v, path)
        .and_then(Value::as_seq)
        .unwrap_or_else(|| panic!("no list {path}"))
        .iter()
        .filter_map(|e| e.as_str().map(ToString::to_string))
        .collect()
}

// layering_test.go: TestLoad_ProjectInheritsHomeKeys / _ProjectOverridesHomeKey
#[test]
fn test_the_project_inherits_the_homes_keys_and_overrides_the_ones_it_sets() {
    let v = merged(&[
        "schema_version: 8\neditor:\n  command: home-editor\nworkspace: worktree\n",
        "schema_version: 8\neditor:\n  command: project-editor\n",
    ]);
    assert_eq!(text(&v, "editor.command"), "project-editor");
    assert_eq!(text(&v, "workspace"), "worktree");
}

// layering_test.go: TestLoad_HomeAndProjectDeepMergeNestedSection
#[test]
fn test_home_and_project_deep_merge_a_nested_section() {
    let v = merged(&[
        "schema_version: 8\ndelegation:\n  concurrency: 3\n  depth: 2\n",
        "schema_version: 8\ndelegation:\n  depth: 5\n",
    ]);
    assert_eq!(text(&v, "delegation.concurrency"), "3");
    assert_eq!(text(&v, "delegation.depth"), "5");
}

// merge_test.go: TestLoad_ProjectAgentBindingReplacesHomesWholesale and
// TestAgentBindingMergeFunc_ReplacesWholesale / _AgentNamedOnlyByLowerLayerSurvives
#[test]
fn test_a_project_agent_binding_replaces_the_homes_of_the_same_name_wholesale() {
    let v = merged(&[
        "schema_version: 8\nagents:\n  coder:\n    engine: claude-code\n    profiles: [h]\n    runtime: host\n  home-only:\n    engine: claude-fast\n",
        "schema_version: 8\nagents:\n  coder:\n    profiles: [p]\n",
    ]);
    assert_eq!(seq(&v, "agents.coder.profiles"), ["p"]);
    assert!(
        get(&v, "agents.coder.engine").is_none(),
        "no field of the home's binding survives"
    );
    assert!(get(&v, "agents.coder.runtime").is_none());
    assert_eq!(
        text(&v, "agents.home-only.engine"),
        "claude-fast",
        "an agent only the lower layer names survives"
    );
}

// merge_test.go: TestAgentBindingMergeFunc_NonAgentKeysStillDeepMerge
#[test]
fn test_keys_other_than_agents_still_deep_merge() {
    let v = merged(&[
        "schema_version: 8\nconfig:\n  defaults:\n    primary: a\nagents:\n  x:\n    engine: l\n",
        "schema_version: 8\nconfig:\n  defaults:\n    fast: b\n",
    ]);
    assert_eq!(text(&v, "config.defaults.primary"), "a");
    assert_eq!(text(&v, "config.defaults.fast"), "b");
    assert_eq!(text(&v, "agents.x.engine"), "l");
}

// merge_test.go: TestLayerMergeFunc_CompanionsUnionAcrossLayers /
// _ProjectCannotRemoveAHomeCompanion / TestLoad_ProjectCompanionsAddToHomes,
// on the registration as a map of names (generation 9, OD-10).
#[test]
fn test_companions_union_across_layers_and_a_project_cannot_remove_one() {
    let v = merged(&[
        "companions:\n  acme: {}\n  beta: {state: disabled}\n",
        "companions:\n  beta: {}\n  gamma: {}\n",
    ]);
    let names: Vec<&str> = v
        .get("companions")
        .and_then(Value::as_map)
        .unwrap()
        .iter()
        .map(|(k, _)| k.as_str())
        .collect();
    assert_eq!(names, ["acme", "beta", "gamma"]);
    assert_eq!(text(&v, "companions.beta.state"), "disabled");
    let kept = merged(&["companions:\n  acme: {}\n", "companions: {}\n"]);
    assert_eq!(
        kept.get("companions")
            .and_then(Value::as_map)
            .map(<[_]>::len),
        Some(1),
        "an empty project map does not unregister"
    );
}

#[test]
fn test_a_companions_state_is_the_highest_layers_that_says_one() {
    let v = merged(&[
        "companions:\n  beta: {state: disabled}\n",
        "companions:\n  beta: {state: enabled}\n",
    ]);
    assert_eq!(text(&v, "companions.beta.state"), "enabled");
    let v = merged(&[
        "companions:\n  beta: {state: disabled}\n",
        "editor:\n  command: vim\n",
    ]);
    assert_eq!(text(&v, "companions.beta.state"), "disabled");
}

// generation 9 registers a companion by name (OD-10): Go's bare-name list of
// generation 7 or 8 is migrated, each name enabled.
#[test]
fn test_a_bare_name_list_at_a_generation_before_9_is_migrated_to_names() {
    for generation in [7, 8] {
        let r = read(&format!(
            "schema_version: {generation}\ncompanions: [acme, ltk]\n"
        ))
        .unwrap();
        assert!(r.notes.is_empty(), "{:?}", r.notes);
        let names: Vec<&str> = r
            .value
            .get("companions")
            .and_then(Value::as_map)
            .unwrap()
            .iter()
            .map(|(k, v)| {
                assert_eq!(
                    *v,
                    Value::Map(Vec::new()),
                    "registered with nothing but its name"
                );
                k.as_str()
            })
            .collect();
        assert_eq!(names, ["acme", "ltk"]);
        assert_eq!(text(&r.value, "schema_version"), "9");
    }
}

#[test]
fn test_an_empty_or_absent_companions_list_loads_and_migrates() {
    for doc in [
        "schema_version: 8\ncompanions: []\neditor:\n  command: vim\n",
        "schema_version: 7\ncompanions:\neditor:\n  command: vim\n",
        "schema_version: 8\neditor:\n  command: vim\n",
    ] {
        let r = read(doc).unwrap();
        assert!(r.notes.is_empty(), "{doc:?}: {:?}", r.notes);
        assert!(r.value.get("companions").is_none(), "{doc:?}");
        assert_eq!(text(&r.value, "schema_version"), "9");
    }
}

#[test]
fn test_a_companions_map_at_generation_8_or_9_is_left_to_the_schema() {
    // A mapping is not migrated: the schema judges the shape.
    let r = read("schema_version: 8\ncompanions:\n  acme: {command: [a]}\n").unwrap();
    assert!(r.notes.is_empty());
    assert_eq!(seq(&r.value, "companions.acme.command"), ["a"]);
    // A list at the current generation is a schema violation, not a note.
    let r = read("schema_version: 9\ncompanions: [acme]\n").unwrap();
    assert!(r.notes.is_empty());
    assert!(
        r.value.get("companions").and_then(Value::as_seq).is_some(),
        "a current file is not migrated: the schema judges the list"
    );
}

// merge_with: lists the higher layer sets replace the lower's (koanf's default)
#[test]
fn test_other_lists_are_replaced_not_joined() {
    let v = merged(&["editor:\n  args: [a, b]\n", "editor:\n  args: [c]\n"]);
    assert_eq!(seq(&v, "editor.args"), ["c"]);
}

// config_version_refusal_test.go: a generation this build cannot migrate is a
// note naming it, and the layer is read as written
#[test]
fn test_a_newer_older_or_unreadable_stamp_is_a_note_and_the_layer_is_read_as_written() {
    let newer = read("schema_version: 10\neditor:\n  command: vim\n").unwrap();
    assert_eq!(
        newer.notes,
        [Note::Newer {
            found: 10,
            current: u64::from(CURRENT)
        }]
    );
    assert_eq!(text(&newer.value, "editor.command"), "vim");

    let older = read("schema_version: 6\neditor:\n  command: vim\n").unwrap();
    assert_eq!(
        older.notes,
        [Note::Older {
            found: 6,
            oldest: u64::from(OLDEST)
        }]
    );

    let unstamped = read("editor:\n  command: vim\n").unwrap();
    assert_eq!(
        unstamped.notes,
        [Note::Older {
            found: 0,
            oldest: u64::from(OLDEST)
        }]
    );

    let not_int = read("schema_version: abc\n").unwrap();
    assert_eq!(not_int.notes, [Note::NotInteger]);

    let current = read("schema_version: 8\neditor:\n  command: vim\n").unwrap();
    assert!(current.notes.is_empty());
}

// v8/step_test.go: TestStep_DropsEssenceMaxChars and
// TestStep_EmptiedSectionLeavesAndAbsentKeyIsUntouched
#[test]
fn test_generation_7_drops_essence_max_chars_and_the_section_it_empties() {
    let r = read(
        "schema_version: 7\nconfig:\n    content_form: original\n    essence_max_chars: 4096\n",
    )
    .unwrap();
    assert!(r.notes.is_empty(), "7 is migrated, not noted");
    assert_eq!(text(&r.value, "config.content_form"), "original");
    assert!(get(&r.value, "config.essence_max_chars").is_none());
    assert_eq!(
        text(&r.value, "schema_version"),
        "9",
        "the stamp follows the migration"
    );

    let emptied = read("schema_version: 7\nconfig:\n    essence_max_chars: 4096\n").unwrap();
    assert!(
        get(&emptied.value, "config").is_none(),
        "a section the key alone filled leaves with it"
    );

    let untouched = read("schema_version: 7\nworkspace: worktree\n").unwrap();
    assert_eq!(text(&untouched.value, "workspace"), "worktree");
    // Not a mapping: left for the strict decode to refuse, never dropped.
    let odd = read("schema_version: 7\nconfig: [x]\n").unwrap();
    assert!(get(&odd.value, "config").and_then(Value::as_seq).is_some());
}

// confload_test.go: TestLoad_WarnsWhenAConfigFileExistsButDefinesNoKeys and
// _DoesNotWarnForAbsentOrPopulatedLayers
#[test]
fn test_a_file_with_no_keys_is_a_note() {
    for empty in ["", "# only a comment\n"] {
        let r = read(empty).unwrap();
        assert!(r.notes.contains(&Note::Keyless), "{empty:?}: {:?}", r.notes);
    }
    assert!(!read("schema_version: 8\n")
        .unwrap()
        .notes
        .contains(&Note::Keyless));
}

// configload layers.go loadConfigLayer: text that is not YAML is refused
#[test]
fn test_text_that_is_not_yaml_is_refused() {
    let err = read("::: not: [valid yaml\n\tbroken").expect_err("not YAML");
    assert!(
        matches!(err, ConfigError::Syntax { .. }),
        "{}",
        err.english()
    );
}

// merge_test.go: a null in a higher layer leaves the lower layer's value, and
// adds no key of its own
#[test]
fn test_a_null_in_a_higher_layer_neither_replaces_nor_adds() {
    let m = merged(&["workspace: worktree\n", "workspace:\nsession_reap_age:\n"]);
    assert_eq!(text(&m, "workspace"), "worktree");
    assert!(get(&m, "session_reap_age").is_none(), "{m:?}");
}

// ---------------------------------------------------------------- characterization
//
// Pinned before the layer mechanics may move (A0): which note each kind of
// text gives, and what comes back with it.

fn notes_of(text: &str) -> Vec<Note> {
    read(text).expect("readable").notes
}

#[test]
fn test_the_generation_boundaries_give_these_notes_exactly() {
    assert_eq!(
        notes_of("schema_version: 10\nk: v\n"),
        [Note::Newer {
            found: 10,
            current: u64::from(CURRENT)
        }]
    );
    assert_eq!(
        notes_of("schema_version: 6\nk: v\n"),
        [Note::Older {
            found: 6,
            oldest: u64::from(OLDEST)
        }]
    );
    assert_eq!(
        notes_of("k: v\n"),
        [Note::Older {
            found: 0,
            oldest: u64::from(OLDEST)
        }],
        "an unstamped file is generation 0"
    );
    assert!(notes_of("schema_version: 7\nk: v\n").is_empty());
    assert!(notes_of("schema_version: 8\nk: v\n").is_empty());
    assert_eq!(notes_of("schema_version: abc\nk: v\n"), [Note::NotInteger]);
    assert_eq!(notes_of("schema_version: -1\nk: v\n"), [Note::NotInteger]);
    assert_eq!(notes_of("schema_version: [8]\nk: v\n"), [Note::NotInteger]);
}

#[test]
fn test_a_stamp_with_spaces_around_its_number_is_read() {
    assert!(notes_of("schema_version: \" 8 \"\nk: v\n").is_empty());
}

#[test]
fn test_a_layer_that_gave_a_note_is_read_as_written_and_a_current_one_is_stamped() {
    let newer = read("schema_version: 10\nk: v\n").expect("readable");
    assert_eq!(
        newer.value.get("schema_version").and_then(Value::as_str),
        Some("10")
    );
    let old = read("schema_version: 7\nk: v\n").expect("readable");
    assert_eq!(
        old.value.get("schema_version").and_then(Value::as_str),
        Some("9"),
        "migrated in memory, stamped with the current generation"
    );
}

#[test]
fn test_an_empty_text_a_comment_and_a_null_are_all_keyless() {
    for text in ["", "# only a comment\n", "~\n", "{}\n"] {
        let read = read(text).expect("readable");
        assert_eq!(read.notes, [Note::Keyless], "{text:?}");
        assert_eq!(read.value, Value::Map(Vec::new()), "{text:?}");
    }
}

#[test]
fn test_text_that_is_a_list_or_a_scalar_is_not_a_mapping() {
    for text in ["- a\n- b\n", "just words\n", "3\n"] {
        assert!(
            matches!(read(text), Err(ConfigError::NotAMapping)),
            "{text:?}"
        );
    }
}

#[test]
fn test_merging_no_layers_is_an_empty_mapping_and_a_non_mapping_layer_adds_nothing() {
    assert_eq!(merge_layers(&[]), Value::Map(Vec::new()));
    let scalar_layer = Value::Scalar("x".to_string());
    assert_eq!(merge_layers(&[scalar_layer]), Value::Map(Vec::new()));
}

// Only generation 7 migrates the retired key away: a generation 8 file that
// still has it keeps it, for the schema to refuse as unknown.
#[test]
fn test_generation_8_keeps_essence_max_chars_for_the_schema_to_judge() {
    let r = read("schema_version: 8\nconfig:\n  essence_max_chars: 4096\n").unwrap();
    assert_eq!(text(&r.value, "config.essence_max_chars"), "4096");
}
