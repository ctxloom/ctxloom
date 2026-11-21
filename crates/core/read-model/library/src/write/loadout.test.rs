use super::*;

/// The document Go's `cmd/ctxloom/loadout.yaml` ships, its structure and
/// none of the owner's text (D24, D8).
const DOCUMENT: &str = "run:\n  version: 1.0.0\n  tags:\n    - core\n    - mcp\n    - isolation\n  author: ctxloom\n  mcp:\n    ctxloom:\n      served_by: session-endpoint\n";

#[test]
fn test_the_loadout_is_the_structure_and_none_of_the_owners_text() {
    assert_eq!(document(&own()), DOCUMENT);
}

#[test]
fn test_ctxloom_s_own_loadout_is_its_run_bundle_and_its_one_server() {
    let own = own();
    assert_eq!(own.version, "1.0.0");
    assert_eq!(own.tags, ["core", "mcp", "isolation"]);
    assert_eq!(own.author, "ctxloom");
    assert_eq!(
        own.mcp,
        [OwnServer {
            id: "ctxloom".into(),
            served_by: "session-endpoint".into(),
        }]
    );
}

#[test]
fn test_the_loadout_run_bundle_is_a_bundle() {
    let loadout = crate::loadout::parse_loadout(&document(&own())).unwrap();
    let bundle = loadout.run;
    assert_eq!(bundle.version, "1.0.0");
    assert_eq!(bundle.author, "ctxloom");
    assert_eq!(bundle.tags, ["core", "mcp", "isolation"]);
    assert_eq!(bundle.mcp["ctxloom"].served_by, "session-endpoint");
    assert!(bundle.mcp["ctxloom"].command.is_empty());
    assert!(bundle.fragments.is_empty());
    assert!(loadout.init.is_zero(), "ctxloom declares no INIT loadout");
}

#[test]
fn test_a_value_yaml_would_read_otherwise_is_quoted_and_reads_back() {
    let odd = OwnLoadout {
        version: "1.0".into(),
        tags: vec!["a: b".into(), "#c".into(), String::new()],
        author: "x \"y\"".into(),
        mcp: vec![OwnServer {
            id: "two words".into(),
            served_by: "session-endpoint".into(),
        }],
    };
    let text = document(&odd);
    let doc = angzarr_yaml::parse(&text).unwrap();
    let run = doc.get("run").unwrap();
    assert_eq!(
        run.get("version").and_then(angzarr_yaml::Value::as_str),
        Some("1.0")
    );
    assert_eq!(
        run.get("author").and_then(angzarr_yaml::Value::as_str),
        Some("x \"y\"")
    );
    let tags: Vec<&str> = run
        .get("tags")
        .and_then(angzarr_yaml::Value::as_seq)
        .unwrap()
        .iter()
        .filter_map(angzarr_yaml::Value::as_str)
        .collect();
    assert_eq!(tags, ["a: b", "#c", ""]);
    assert_eq!(
        run.get("mcp")
            .and_then(|m| m.get("two words"))
            .and_then(|s| s.get("served_by"))
            .and_then(angzarr_yaml::Value::as_str),
        Some("session-endpoint")
    );
}

#[test]
fn test_a_loadout_with_no_tags_or_servers_writes_neither_key() {
    let bare = OwnLoadout {
        version: "1.0.0".into(),
        author: "ctxloom".into(),
        ..OwnLoadout::default()
    };
    assert_eq!(
        document(&bare),
        "run:\n  version: 1.0.0\n  author: ctxloom\n"
    );
}
