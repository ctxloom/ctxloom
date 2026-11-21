//! A checkout's harp: read without minting, minted once, kept.

use super::*;
use crate::fixtures::{fs, root};

#[test]
fn a_tree_never_read_has_no_checkout_harp_and_reading_mints_none() {
    let root = root();
    let app = root.root().join(".ctxloom");
    let fs = fs();
    assert_eq!(checkout_harp(fs.as_ref(), &app).unwrap(), None);
    assert!(!fs.exists(&checkout_file(&app)).unwrap());
}

#[test]
fn the_first_read_mints_the_harp_into_state_checkout_and_every_later_one_keeps_it() {
    let root = root();
    let app = root.root().join(".ctxloom");
    let fs = fs();
    let harp = mint_checkout_harp(fs.as_ref(), &app, || "brisk-copper-moth".to_string()).unwrap();
    assert_eq!(harp, "brisk-copper-moth");
    assert_eq!(checkout_file(&app), app.join("state").join("checkout"));
    assert_eq!(
        fs.read_to_string(&checkout_file(&app)).unwrap(),
        "brisk-copper-moth\n"
    );
    let again = mint_checkout_harp(fs.as_ref(), &app, || unreachable!("minted once")).unwrap();
    assert_eq!(again, harp);
    assert_eq!(checkout_harp(fs.as_ref(), &app).unwrap(), Some(harp));
}

#[test]
fn a_mint_cut_short_is_minted_again() {
    let root = root();
    let app = root.root().join(".ctxloom");
    let fs = fs();
    fs.create_dir_all(&app.join("state"), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &checkout_file(&app),
        b"\n",
        Perms::Exact(Mode::from_bits(0o644)),
    )
    .unwrap();
    assert_eq!(checkout_harp(fs.as_ref(), &app).unwrap(), None);
    let harp = mint_checkout_harp(fs.as_ref(), &app, || "calm-amber-fox".to_string()).unwrap();
    assert_eq!(harp, "calm-amber-fox");
    assert_eq!(
        checkout_harp(fs.as_ref(), &app).unwrap().as_deref(),
        Some("calm-amber-fox")
    );
}

#[test]
fn a_checkout_file_that_holds_no_harp_is_a_failure_never_another_name() {
    let root = root();
    let app = root.root().join(".ctxloom");
    let fs = fs();
    fs.create_dir_all(&app.join("state"), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &checkout_file(&app),
        b"a/b\n",
        Perms::Exact(Mode::from_bits(0o644)),
    )
    .unwrap();
    assert!(matches!(
        checkout_harp(fs.as_ref(), &app),
        Err(LibError::Checkout { .. })
    ));
    assert!(matches!(
        mint_checkout_harp(fs.as_ref(), &app, || "calm-amber-fox".to_string()),
        Err(LibError::Checkout { .. })
    ));
}

#[test]
fn a_minted_name_that_is_no_harp_is_refused_and_nothing_is_written() {
    let root = root();
    let app = root.root().join(".ctxloom");
    let fs = fs();
    assert!(mint_checkout_harp(fs.as_ref(), &app, || "..".to_string()).is_err());
    assert!(!fs.exists(&checkout_file(&app)).unwrap());
}

#[test]
fn a_checkouts_tree_is_named_under_its_project() {
    let project = ProjectName::parse("projects/amber-quiet-heron").unwrap();
    assert_eq!(
        checkout_tree(&project, "brisk-copper-moth")
            .unwrap()
            .to_string(),
        "projects/amber-quiet-heron/checkouts/brisk-copper-moth/library"
    );
    assert!(checkout_tree(&project, "").is_err());
}
