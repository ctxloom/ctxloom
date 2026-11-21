//! A tree's name and its root.

use super::*;

fn project() -> ProjectName {
    ProjectName::parse("projects/amber-quiet-heron").unwrap()
}

#[test]
fn a_checkouts_tree_is_named_under_its_project_and_reads_back() {
    let name = TreeName::checkout(project(), "brisk-copper-moth").unwrap();
    let text = "projects/amber-quiet-heron/checkouts/brisk-copper-moth/library";
    assert_eq!(name.to_string(), text);
    assert_eq!(TreeName::parse_checkout(text), Some(name.clone()));
    assert_eq!(name.kind(), TreeKind::Checkout);
    assert_eq!(name.checkout_harp(), "brisk-copper-moth");
}

#[test]
fn nothing_but_a_checkouts_tree_parses_as_one() {
    for name in [
        "projects/amber-quiet-heron/checkouts/brisk-copper-moth",
        "projects/amber-quiet-heron/checkouts/brisk-copper-moth/libraries",
        "projects/amber-quiet-heron/sessions/brisk-copper-moth/library",
        "users/me/library",
        "projects/amber-quiet-heron/checkouts/../library",
        "projects/../checkouts/brisk-copper-moth/library",
        "projects/amber-quiet-heron/checkouts//library",
    ] {
        assert_eq!(TreeName::parse_checkout(name), None, "{name}");
    }
}

#[test]
fn a_checkout_or_home_harp_must_be_a_harp() {
    assert!(TreeName::checkout(project(), "..").is_err());
    assert!(TreeName::home("").is_err());
    assert!(TreeName::home(" padded").is_err());
}

#[test]
fn a_checkouts_root_is_the_uuidv5_of_its_name() {
    let name = TreeName::checkout(project(), "brisk-copper-moth").unwrap();
    let expected = sessions::root_of(&name.to_string());
    assert_eq!(name.root().value.as_ref(), expected.as_bytes());
    assert_eq!(name.root(), tree_root(&name.to_string()));
}

#[test]
fn every_home_is_users_me_library_and_each_is_rooted_on_its_own_harp() {
    let one = TreeName::home("brisk-copper-moth").unwrap();
    let other = TreeName::home("calm-amber-fox").unwrap();
    assert_eq!(one.to_string(), HOME_TREE);
    assert_eq!(other.to_string(), "users/me/library");
    assert_eq!(one.kind(), TreeKind::Home);
    assert_eq!(one.checkout_harp(), "brisk-copper-moth");
    assert_ne!(one.root(), other.root());
    assert_eq!(
        one.root(),
        tree_root("users/me/checkouts/brisk-copper-moth/library")
    );
    assert_ne!(one.root(), tree_root(HOME_TREE));
}

#[test]
fn a_names_kind_is_the_homes_only_for_the_homes_name() {
    assert_eq!(kind_of("users/me/library"), TreeKind::Home);
    assert_eq!(
        kind_of("projects/amber-quiet-heron/checkouts/brisk-copper-moth/library"),
        TreeKind::Checkout
    );
}

#[test]
fn the_checkout_harp_is_kept_in_state_out_of_git() {
    assert_eq!(CHECKOUT_FILE, "state/checkout");
}
