//! The stable session id (D5 Q1/Q4): the OWNER'S RULING, not Go's. Committed
//! red and WITHHELD from implementation until the owner reviews them. A
//! session's stream root is an id minted at creation, so a harp is only its
//! name and a mint is "refused on a stream with history" (plan 3.1, and the
//! S4 red tests: "A mint on a stream with history is refused").

use super::tests_support::*;
use crate::{SessionEvent, SessionRejection};

#[test]
fn a_mint_on_a_stream_with_history_is_refused_session_exists_and_records_nothing() {
    let mut session = Session::minted();
    let before = session.pages().len();
    let refusal = session.run(mint("other-name-here")).unwrap_err();
    assert_eq!(
        refusal,
        said(&SessionRejection::SessionExists {
            harp: HARP.to_string()
        })
    );
    assert_eq!(session.pages().len(), before, "a refusal records nothing");
    assert_eq!(session.state().harp, HARP, "the session held is untouched");
}

// A purge keeps the stream and leaves a tombstone, which the framework folds
// into the default state: a stream whose last event is a tombstone holds no
// session, so its harp is free, though the stream has events.
#[test]
fn a_mint_on_a_stream_closed_by_a_purge_is_accepted_and_is_a_new_session() {
    let mut session = Session::minted();
    assert!(session.purge() > 0, "events deleted");
    let events = session.run(mint(HARP)).expect("the harp is free again");
    assert!(
        events
            .iter()
            .any(|e| matches!(e, SessionEvent::SessionMinted(_))),
        "{events:?}"
    );
    assert_eq!(session.state().harp, HARP);
}
