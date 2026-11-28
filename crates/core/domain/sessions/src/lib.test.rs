//! The rules that need no stream: the payloads on the wire and the catalog.
//! The rules a stream decides are `aggregate.test.rs`'s, through
//! angzarr-embedded's kernel.

use crate::{diagnostic_samples, EN_US, NAMES_DOMAIN, SESSION_DOMAIN};

// The domains are spelled as the markup takes them, literals in the
// `#[aggregate]` attributes; the constants are what the rest of ctxloom reads.
#[test]
fn the_domain_names_are_the_ones_the_plan_gives() {
    assert_eq!(SESSION_DOMAIN, "sessions");
    assert_eq!(NAMES_DOMAIN, "session-names");
}

// Every code is prefixed with the crate's name and has a message that uses
// exactly its args (the binary's catalog test holds the same for the whole
// graph; this one fails in this crate first).
#[test]
fn every_diagnostic_has_a_message_that_uses_exactly_its_args() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "sessions");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("sessions-"), "{code} is prefixed");
        let line = catalog
            .lines()
            .find(|l| l.starts_with(&format!("{code} =")))
            .unwrap_or_else(|| panic!("{code} has no message"));
        let mut args: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
        args.sort_unstable();
        let mut used: Vec<&str> = line
            .split('$')
            .skip(1)
            .map(|rest| {
                rest.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '-'))
                    .next()
                    .unwrap()
            })
            .collect();
        used.sort_unstable();
        used.dedup();
        assert_eq!(args, used, "{code}");
    }
}

// Every sample says its code (Display is the code then the args, never
// English), its catalog message uses exactly its args, and the samples are
// not empty.
#[test]
fn every_sample_displays_its_code_and_its_message_uses_exactly_its_args() {
    let catalogs: Vec<&str> = vec![EN_US.1];
    let samples = diagnostic_samples();
    assert!(!samples.is_empty());
    for sample in samples {
        let shown = format!("{sample}");
        assert!(shown.starts_with(sample.code()), "{shown}");
        let names: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
        assert_eq!(shown.contains('='), !names.is_empty(), "{shown}");
        let line = catalogs
            .iter()
            .flat_map(|c| c.lines())
            .find(|l| l.starts_with(&format!("{} =", sample.code())))
            .unwrap_or_else(|| panic!("{} has no message", sample.code()));
        let mut used: Vec<&str> = line
            .split('$')
            .skip(1)
            .map(|r| {
                r.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '-'))
                    .next()
                    .unwrap()
            })
            .collect();
        used.sort_unstable();
        used.dedup();
        let mut names = names;
        names.sort_unstable();
        assert_eq!(names, used, "{}", sample.code());
    }
}

// Step 10c U13: a session's loadout and a redelivery are facts keyed by
// what they are of, so a repeat is a DUPLICATE: the loadout by its session
// and incarnation, a redelivery by its session and a hash of the change's
// cause (the same cause, the same key; another, another).
#[test]
fn a_loadout_and_a_redelivery_are_keyed_by_what_they_are_of() {
    let session = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b";
    assert_eq!(
        crate::loadout_key(session, 2),
        format!("loadout:{session}:2")
    );
    let one = crate::redelivery_key(session, "obs:tree:a");
    assert!(one.starts_with(&format!("reinject:{session}:")), "{one}");
    assert!(!one.contains("obs:tree:a"), "the cause hashed: {one}");
    assert_eq!(one, crate::redelivery_key(session, "obs:tree:a"));
    assert_ne!(one, crate::redelivery_key(session, "obs:tree:b"));
    assert_ne!(one, crate::redelivery_key("other", "obs:tree:a"));
}

// A fact's book is a Fact body on its session's stream, keyed by the
// external id it is given.
#[test]
fn a_facts_book_carries_it_under_its_key() {
    use angzarr_embedded::v1::command_page;
    let cover = angzarr_embedded::v1::Cover {
        domain: SESSION_DOMAIN.to_string(),
        root: None,
        edition: None,
    };
    let fact = crate::RecordLoadout {
        loadout: crate::SessionLoadout {
            agent: String::new(),
            profiles: Vec::new(),
            bundles: Vec::new(),
        },
        incarnation: 1,
        covered: Vec::new(),
    };
    let book = crate::fact_book(cover.clone(), &fact, "loadout:s:1");
    assert_eq!(book.cover, Some(cover));
    let Some(command_page::Body::Fact(body)) = book.page.and_then(|page| page.body) else {
        panic!("a fact")
    };
    assert_eq!(body.origin.unwrap().external_id, "loadout:s:1");
    assert!(body.payload.unwrap().type_url.ends_with("RecordLoadout"));
}

// Step 10c U13 rework: a redelivery is keyed by the positions it covers,
// spelled one way whatever their order; the owed keys name the tree and the
// sequence.
#[test]
fn positions_are_one_text_whatever_their_order_and_keys_name_them() {
    let at = |tree: &str, sequence| crate::TreePosition {
        tree: tree.to_string(),
        sequence,
    };
    let text = crate::positions_text(&[
        at("users/me/library", 2),
        at("projects/p/checkouts/c/library", 9),
    ]);
    assert_eq!(text, "projects/p/checkouts/c/library@9,users/me/library@2");
    assert_eq!(
        crate::positions_text(&[
            at("projects/p/checkouts/c/library", 9),
            at("users/me/library", 2)
        ]),
        text
    );
    assert_ne!(
        crate::redelivery_key("s", &text),
        crate::redelivery_key("s", "projects/p/checkouts/c/library@9,users/me/library@3")
    );
    assert_eq!(
        crate::owed_key("s", "users/me/library", 4),
        "owed:s:users/me/library@4"
    );
}

// Step 10e Stage 2a: the owed restart is part of the session's wire state
// (the daemon's board and `GetSession` read it), both ways.
#[test]
fn the_owed_restart_crosses_the_wire_with_the_state() {
    let state = crate::SessionState {
        started_at: angzarr_embedded::Timestamp::default(),
        restart_owed: Some(crate::RestartOwed {
            reason: crate::RestartReason::Context,
            incarnation: 3,
            note: "n".to_string(),
        }),
        ..crate::SessionState::default()
    };
    let wire = sessions_proto::v1::SessionState::from(state.clone());
    assert_eq!(wire.restart_owed.as_ref().map(|o| o.incarnation), Some(3));
    assert_eq!(crate::SessionState::try_from(wire).unwrap(), state);
}
