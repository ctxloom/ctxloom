//! The observation's key: ids alone, chained on the last cause.

use sha2::{Digest, Sha256};

use super::*;

const TREE: &str = "projects/amber-quiet-heron/checkouts/brisk-copper-moth/library";

fn dirty(path: &str, oid: &str) -> DirtyPath {
    DirtyPath {
        path: path.to_string(),
        oid: oid.to_string(),
    }
}

#[test]
fn the_key_is_the_tree_the_hashed_cause_the_head_and_the_hashed_dirty_lines() {
    let key = observation_key(TREE, "c-0", "h-1", &[dirty("b", "2"), dirty("a", "1")]);
    let cause = hex::encode(Sha256::digest(b"c-0"));
    let lines = hex::encode(Sha256::digest(b"a\x001\nb\x002\n"));
    assert_eq!(key, format!("obs:{TREE}:{cause}:h-1:{lines}"));
}

#[test]
fn the_dirty_set_is_keyed_in_path_order_whatever_order_it_came_in() {
    assert_eq!(
        observation_key(TREE, "c", "h", &[dirty("b", "2"), dirty("a", "1")]),
        observation_key(TREE, "c", "h", &[dirty("a", "1"), dirty("b", "2")]),
    );
}

#[test]
fn each_part_of_what_was_seen_changes_the_key() {
    let base = observation_key(TREE, "c", "h", &[dirty("a", "1")]);
    for other in [
        observation_key("users/me/library", "c", "h", &[dirty("a", "1")]),
        observation_key(TREE, "c2", "h", &[dirty("a", "1")]),
        observation_key(TREE, "c", "h2", &[dirty("a", "1")]),
        observation_key(TREE, "c", "h", &[dirty("a", "2")]),
        observation_key(TREE, "c", "h", &[dirty("b", "1")]),
        observation_key(TREE, "c", "h", &[]),
    ] {
        assert_ne!(base, other);
    }
}

#[test]
fn a_path_and_its_oid_cannot_be_confused_across_the_separator() {
    assert_ne!(
        observation_key(TREE, "c", "h", &[dirty("a", "b1")]),
        observation_key(TREE, "c", "h", &[dirty("ab", "1")]),
    );
}

#[test]
fn a_to_b_to_a_to_b_never_repeats_a_key_because_each_chains_on_the_last() {
    // Each key becomes the next observation's last cause, as the record
    // folds it: the dirty sets repeat, the keys never do.
    let a: Vec<DirtyPath> = Vec::new();
    let b = vec![dirty("content/bundles/v2/demo/bundle.yaml", "b0b")];
    let mut cause = "baseline".to_string();
    let mut keys = Vec::new();
    for seen in [&b, &a, &b, &a, &b] {
        let key = observation_key(TREE, &cause, "h", seen);
        keys.push(key.clone());
        cause = key;
    }
    let mut unique = keys.clone();
    unique.sort();
    unique.dedup();
    assert_eq!(unique.len(), keys.len(), "{keys:?}");
}

#[test]
fn an_observations_book_is_a_fact_on_the_trees_stream_keyed_by_what_it_saw() {
    use angzarr_embedded::markup::DomainMessage as _;
    use angzarr_embedded::v1::command_page;
    let project = sessions::ProjectName::parse("projects/amber-quiet-heron").unwrap();
    let tree = TreeName::checkout(project, "brisk-copper-moth").unwrap();
    let fact = RecordTreeObserved {
        name: tree.to_string(),
        kind: crate::TreeKind::Checkout,
        base_cause: "c-0".to_string(),
        head_before: "h-0".to_string(),
        head_after: "h-1".to_string(),
        tree_after: "t".to_string(),
        changes: Vec::new(),
        dirty_after: vec![dirty("a", "1")],
        baseline: false,
    };
    let book = observation_book(&tree, &fact);
    let cover = book.cover.unwrap();
    assert_eq!(cover.domain, crate::LIBRARY_DOMAIN);
    assert_eq!(cover.root, Some(tree.root()));
    let Some(command_page::Body::Fact(sent)) = book.page.unwrap().body else {
        panic!("a fact")
    };
    let origin = sent.origin.unwrap();
    assert_eq!(
        origin.external_id,
        observation_key(TREE, "c-0", "h-1", &[dirty("a", "1")])
    );
    assert!(!origin.description.is_empty());
    let payload = sent.payload.unwrap();
    let back = <library_domain_proto::v1::RecordTreeObserved as prost::Message>::decode(
        payload.value.as_slice(),
    )
    .unwrap();
    assert_eq!(back, fact.to_wire());
}

// Step 10c U13 rework: a content id is the SHA-256 of the bytes, in hex.
#[test]
fn a_content_id_is_the_sha256_of_its_bytes() {
    assert_eq!(
        crate::content_id(b"abc"),
        "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    );
    assert_ne!(crate::content_id(b"abc"), crate::content_id(b"abd"));
}
