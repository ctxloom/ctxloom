//! A command's refusal, read back by its TYPE as the domain's rejection, and
//! what a person is told for each: the code, the args, the exit code.
//!
//! Every refusal the sessions and name-claim commands raise is provoked for
//! real, through the kernel over an in-memory store, then settled as a use
//! case settles it.

use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::angzarr_ports::memory::MemoryStore;
use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Cover, Uuid};
use angzarr_embedded::{Domain, Handled, OnRetryable, Payload};
use angzarr_embedded_runner::{Refusal, Runner, Unrun};
use sessions::{
    AdoptRotations, ClaimName, ForgetSession, MintSession, NameClaimAggregate, NameClaimCommand,
    ReleaseName, RenameSession, SessionAggregate, SessionCommand, SessionRejection, SetKept,
    StopSession, WithdrawSteer,
};

use super::run;
use crate::bridge::Port;
use crate::fakes::{claim_cover, cover, minted, project, ready, run as send, FakeClock, FakeDelay};
use crate::AppError;

const WHEN: i64 = 1_700_000_000;

/// `command` as a book to the stream `cover`.
fn book(cover: Cover, payload: angzarr_embedded::Any) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(payload),
                handling: None,
            })),
            ..CommandPage::default()
        }),
    }
}

/// What a use case reads of `book` sent to a stream of aggregate `A` in
/// `store`: the kernel's answer, settled by `typed::run`.
fn settle<A>(
    store: &MemoryStore,
    book: CommandBook,
) -> Result<Result<Handled, Refusal<SessionRejection>>, Unrun>
where
    A: Domain<Rejection = angzarr_embedded::markup::TypedRefusal>,
{
    let registry = crate::registry();
    let mut clock = FakeClock::default();
    let mut delay = FakeDelay::default();
    let mut dead_letters = Vec::new();
    let mut runner = Runner {
        registry: &registry,
        clock: &mut Port(&mut clock),
        delay: &mut Port(&mut delay),
        retry: OnRetryable {
            max_retries: 0,
            ..OnRetryable::default()
        },
        dead_letters: &mut dead_letters,
    };
    ready(run::<A, _, _, _>(
        &mut runner,
        store,
        book,
        &Uuid::from_bytes([9; 16]),
    ))
}

/// The session on stream 1, minted, in a fresh store.
fn store_with_session() -> MemoryStore {
    let store = MemoryStore::default();
    minted(&store, 1, "owl", "/work", WHEN);
    store
}

fn to_session(command: impl Into<SessionCommand>) -> CommandBook {
    book(cover(1), command.into().to_any())
}

fn to_claim(harp: &str, command: impl Into<NameClaimCommand>) -> CommandBook {
    book(
        claim_cover("jolly-mute-magma", harp),
        command.into().to_any(),
    )
}

fn mint(harp: &str) -> MintSession {
    MintSession {
        harp: harp.to_string(),
        project_id: "jolly-mute-magma".to_string(),
        project_dir: "/work".to_string(),
        output_dir: String::new(),
        engine: "mock".to_string(),
        origin: sessions::Origin::Session,
        started_at: None,
        depth: 0,
    }
}

/// A fact to `cover`, under `external_id`.
fn fact(cover: Cover, payload: angzarr_embedded::Any, external_id: &str) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(angzarr_embedded::v1::Fact {
                payload: Some(payload),
                origin: Some(angzarr_embedded::v1::FactOrigin {
                    external_id: external_id.to_string(),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

/// A session fact, applied.
fn applied(
    store: &MemoryStore,
    message: impl angzarr_embedded::markup::DomainMessage,
    external_id: &str,
) {
    let book = fact(
        cover(1),
        angzarr_embedded::markup::pack_message(&message),
        external_id,
    );
    match settle::<SessionAggregate>(store, book) {
        Ok(Ok(Handled::Applied(_))) => {}
        _ => panic!("the fact {external_id} was not applied"),
    }
}

/// A name taken by `session`.
fn taken(store: &MemoryStore, harp: &str, session: &str) {
    crate::fakes::claim(store, "jolly-mute-magma", harp, session).expect("taken");
}

fn rename(harp: &str) -> RenameSession {
    RenameSession {
        harp: harp.to_string(),
        output_dir: String::new(),
        lock: sessions::LockEvidence::Free,
    }
}

/// One refusal provoked, and what it came to: the rejection read back by the
/// refusal's type, and what the person is told.
struct Told {
    rejection: SessionRejection,
    code: String,
    args: Vec<(String, String)>,
    exit: u8,
}

fn told(store: &MemoryStore, aggregate_is_claim: bool, book: CommandBook) -> Told {
    let answered = match aggregate_is_claim {
        true => settle::<NameClaimAggregate>(store, book),
        false => settle::<SessionAggregate>(store, book),
    };
    let Ok(Err(refusal)) = answered else {
        panic!("a refusal");
    };
    let rejection = refusal.rejection.clone().expect("a refusal of the domain");
    let error = AppError::from(refusal);
    let mut args: Vec<(String, String)> = error
        .args()
        .into_iter()
        .map(|(name, arg)| {
            let text = match arg {
                angzarr_diagnostic::Arg::Str(text) => text,
                angzarr_diagnostic::Arg::Uint(n) => n.to_string(),
                angzarr_diagnostic::Arg::Int(n) => n.to_string(),
                angzarr_diagnostic::Arg::Nested(cause) => cause.code().to_string(),
            };
            (name.to_string(), text)
        })
        .collect();
    args.sort();
    Told {
        rejection,
        code: error.code().to_string(),
        args,
        exit: error.exit_code(),
    }
}

fn expect(told: Told, rejection: SessionRejection, code: &str, args: &[(&str, &str)], exit: u8) {
    assert_eq!(told.rejection, rejection, "{code}");
    assert_eq!(told.code, code);
    let args: Vec<(String, String)> = args
        .iter()
        .map(|(name, value)| (name.to_string(), value.to_string()))
        .collect();
    assert_eq!(told.args, args, "{code}");
    assert_eq!(told.exit, exit, "{code}");
}

/// The table: every refusal of the sessions and name-claim commands, by its
/// type, and what a person is told for it (the stable code, the args, the exit
/// code), whatever status each travels under.
#[test]
fn the_mint_and_rename_refusals_are_told_by_their_type() {
    let long = "x".repeat(30);

    let store = store_with_session();
    expect(
        told(&store, false, to_session(mint("owl"))),
        SessionRejection::SessionExists {
            harp: "owl".to_string(),
        },
        "sessions-session-exists",
        &[("harp", "owl")],
        1,
    );

    expect(
        told(
            &MemoryStore::default(),
            false,
            book(cover(2), SessionCommand::from(mint("a/b")).to_any()),
        ),
        SessionRejection::HarpInvalid {
            harp: "a/b".to_string(),
            reason: "separator".to_string(),
        },
        "sessions-harp-invalid",
        &[("harp", "a/b"), ("reason", "separator")],
        2,
    );

    expect(
        told(&store, false, to_session(rename(&long))),
        SessionRejection::HarpTooLong {
            harp: long.clone(),
            limit: 24,
            length: 30,
        },
        "sessions-harp-too-long",
        &[("harp", long.as_str()), ("length", "30"), ("limit", "24")],
        2,
    );

    expect(
        told(
            &MemoryStore::default(),
            false,
            to_session(SetKept { kept: true }),
        ),
        SessionRejection::NoSession,
        "sessions-no-session",
        &[],
        1,
    );

    let forgotten = store_with_session();
    send(&forgotten, 1, WHEN + 1, ForgetSession {});
    expect(
        told(&forgotten, false, to_session(SetKept { kept: true })),
        SessionRejection::Forgotten {
            harp: "owl".to_string(),
        },
        "sessions-forgotten",
        &[("harp", "owl")],
        1,
    );

    expect(
        told(
            &store,
            false,
            to_session(StopSession {
                reason: "end".to_string(),
                requester_depth: 1,
            }),
        ),
        SessionRejection::ChildCannotStop { depth: 1 },
        "sessions-child-cannot-stop",
        &[("depth", "1")],
        1,
    );

    expect(
        told(
            &store,
            false,
            to_session(WithdrawSteer {
                message_id: "m1".to_string(),
            }),
        ),
        SessionRejection::UnknownMessage {
            message_id: "m1".to_string(),
        },
        "sessions-unknown-message",
        &[("message_id", "m1")],
        1,
    );
}

/// A steer withdrawn after it was delivered.
#[test]
fn a_withdrawal_after_the_delivery_is_told_by_its_type() {
    let delivered = store_with_session();
    applied(
        &delivered,
        sessions::RecordMessageArrived {
            message_id: "m1".to_string(),
            from: "s".to_string(),
            kind: "steer".to_string(),
            body_ref: "b".to_string(),
        },
        "arrived:m1",
    );
    applied(
        &delivered,
        sessions::ClaimMessage {
            message_id: "m1".to_string(),
            claimant: "r".to_string(),
        },
        "claimed:m1",
    );
    applied(
        &delivered,
        sessions::DeliverMessage {
            message_id: "m1".to_string(),
            at_turn: 1,
        },
        "delivered:m1",
    );
    expect(
        told(
            &delivered,
            false,
            to_session(WithdrawSteer {
                message_id: "m1".to_string(),
            }),
        ),
        SessionRejection::AlreadyDelivered {
            message_id: "m1".to_string(),
        },
        "sessions-already-delivered",
        &[("message_id", "m1")],
        1,
    );
}

/// The refusals of the name claims.
#[test]
fn the_name_claim_refusals_are_told_by_their_type() {
    let names = MemoryStore::default();
    taken(&names, "owl", &crate::fakes::id_text(1));
    expect(
        told(
            &names,
            true,
            to_claim(
                "owl",
                ClaimName {
                    project_id: "jolly-mute-magma".to_string(),
                    harp: "owl".to_string(),
                    session: crate::fakes::id_text(2),
                },
            ),
        ),
        SessionRejection::NamesTaken {
            harp: "owl".to_string(),
        },
        "sessions-names-taken",
        &[("harp", "owl")],
        1,
    );
    expect(
        told(
            &names,
            true,
            to_claim(
                "owl",
                ReleaseName {
                    session: crate::fakes::id_text(2),
                },
            ),
        ),
        SessionRejection::NotHolder {
            harp: "owl".to_string(),
        },
        "sessions-not-holder",
        &[("harp", "owl")],
        1,
    );
    expect(
        told(
            &MemoryStore::default(),
            true,
            to_claim(
                "owl",
                ReleaseName {
                    session: crate::fakes::id_text(1),
                },
            ),
        ),
        SessionRejection::NoClaim,
        "sessions-no-claim",
        &[],
        1,
    );
}

/// A refusal that is none of the domain's (the runtime's own) is reported as
/// the runtime made it, with no rejection.
#[test]
fn a_refusal_of_no_domain_has_no_rejection_and_the_runtimes_report() {
    // A command the registry has no handler for, refused by the kernel itself.
    let store = MemoryStore::default();
    let unknown = book(
        cover(1),
        angzarr_embedded::Any {
            type_url: "/toy.v1.Stranger".to_string(),
            value: Vec::new(),
        },
    );
    // Not a retryable failure: the kernel's answer is final, not given up on.
    assert!(matches!(
        settle::<SessionAggregate>(&store, unknown),
        Err(Unrun::Failed(_))
    ));
}

#[test]
fn a_committed_command_is_handled() {
    let store = MemoryStore::default();
    assert!(matches!(
        settle::<SessionAggregate>(&store, to_session(mint("owl"))),
        Ok(Ok(Handled::Applied(_)))
    ));
    assert!(matches!(
        settle::<SessionAggregate>(&store, to_session(mint("owl"))),
        Ok(Ok(Handled::Duplicate(..)))
    ));
}

/// An adopt on no session is refused no-session, as any command to a stream
/// holding none is.
#[test]
fn a_command_to_a_stream_holding_no_session_is_refused_no_session() {
    let refused = told(
        &MemoryStore::default(),
        false,
        to_session(AdoptRotations {
            rotations: Vec::new(),
        }),
    );
    assert_eq!(refused.rejection, SessionRejection::NoSession);
    let _ = project("p");
}

/// A store that could not say is a retryable failure: the kernel's retries
/// spent, the use case gives up at once (and says after how many attempts).
#[test]
fn a_store_that_could_not_say_is_given_up_on_not_failed() {
    use angzarr_embedded::testing::{Fault, FaultyStore};
    let store = FaultyStore::new(MemoryStore::default());
    store.fail(std::iter::repeat_n(Fault::Unavailable, 8));
    let registry = crate::registry();
    let (mut clock, mut delay, mut dead_letters) =
        (FakeClock::default(), FakeDelay::default(), Vec::new());
    let mut runner = Runner {
        registry: &registry,
        clock: &mut Port(&mut clock),
        delay: &mut Port(&mut delay),
        retry: OnRetryable {
            max_retries: 0,
            ..OnRetryable::default()
        },
        dead_letters: &mut dead_letters,
    };
    let answered = ready(run::<SessionAggregate, _, _, _>(
        &mut runner,
        &store,
        to_session(mint("owl")),
        &Uuid::from_bytes([9; 16]),
    ));
    assert!(matches!(answered, Err(Unrun::GaveUp { attempts: 1, .. })));
}
