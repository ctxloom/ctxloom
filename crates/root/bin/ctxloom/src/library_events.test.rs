//! `ctxloom test library-events` (step 10a, QUESTIONS G), the test build's
//! reader of a library tree's record, through the binary: before any read
//! has named a checkout it reads none; once one is named it reads that
//! tree's stream from the one store. What a read records there is U2's
//! (library_changes.feature).

use super::Env;
use crate::tests::files;

#[test]
fn a_home_no_read_has_named_reads_no_tree_and_no_event() {
    let env = Env::new();
    let ran = env.run(&["test", "library-events", "--home", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    let doc = ran.json();
    assert_eq!(doc["tree"], serde_json::Value::Null, "{doc}");
    assert_eq!(doc["events"], serde_json::json!([]), "{doc}");
    assert!(
        !files::exists(env.home().join(".ctxloom/state/checkout")),
        "reading mints no checkout"
    );
}

#[test]
fn a_named_home_is_users_me_library_read_from_the_one_store() {
    let env = Env::new();
    let opened = env.run(&["test", "open-store", "library-events"]);
    assert_eq!(opened.code, 0, "{}", opened.said());
    files::put(
        env.home().join(".ctxloom/state/checkout"),
        "brisk-copper-moth\n",
    );
    let ran = env.run(&["test", "library-events", "--home", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    let doc = ran.json();
    assert_eq!(doc["tree"], "users/me/library", "{doc}");
    assert_eq!(doc["events"], serde_json::json!([]), "{doc}");
}
