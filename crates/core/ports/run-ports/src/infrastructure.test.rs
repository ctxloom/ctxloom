use super::*;

/// Each infrastructure error and the status it travels with, as the gRPC
/// spec names it: the table this root exists to hold once.
fn table() -> [(Infrastructure, Code, Option<&'static str>); 13] {
    [
        (
            Infrastructure::SessionUnknown,
            Code::NotFound,
            Some(NO_SUCH_SESSION),
        ),
        (Infrastructure::Parked, Code::Internal, Some(DEAD_LETTERED)),
        (Infrastructure::Conflict, Code::Aborted, None),
        (Infrastructure::Failed, Code::Internal, None),
        (Infrastructure::Malformed, Code::InvalidArgument, None),
        (Infrastructure::Unimplemented, Code::Unimplemented, None),
        (Infrastructure::Unauthenticated, Code::Unauthenticated, None),
        (Infrastructure::Unavailable, Code::Unavailable, None),
        (Infrastructure::TimedOut, Code::DeadlineExceeded, None),
        (Infrastructure::Exhausted, Code::ResourceExhausted, None),
        (Infrastructure::Unknown, Code::Unknown, None),
        (Infrastructure::DataLoss, Code::DataLoss, None),
        (
            Infrastructure::Unmet,
            Code::FailedPrecondition,
            Some(DIAGNOSED),
        ),
    ]
}

#[test]
fn test_each_infrastructure_error_names_its_status_and_key_once() {
    for (error, code, key) in table() {
        assert_eq!(error.code(), code, "{error:?}");
        assert_eq!(error.status(), code.number(), "{error:?}");
        assert_eq!(error.key(), key, "{error:?}");
    }
}

#[test]
fn test_all_lists_every_infrastructure_error_once() {
    let listed: Vec<_> = table().iter().map(|(error, _, _)| *error).collect();
    assert_eq!(Infrastructure::ALL.to_vec(), listed);
}

#[test]
fn test_each_infrastructure_error_reads_back_from_its_status_and_key() {
    for error in Infrastructure::ALL {
        let keys: Vec<&str> = error.key().into_iter().collect();
        assert_eq!(
            Infrastructure::read(error.status(), &keys),
            Some(error),
            "{error:?}"
        );
    }
}

#[test]
fn test_a_keyed_error_reads_back_from_any_of_the_keys_it_is_given() {
    // A client hands the status's message and its ErrorInfo reason both:
    // the key may be either.
    let code = Code::NotFound.number();
    for keys in [[NO_SUCH_SESSION, ""], ["", NO_SUCH_SESSION]] {
        assert_eq!(
            Infrastructure::read(code, &keys),
            Some(Infrastructure::SessionUnknown)
        );
    }
    let internal = Code::Internal.number();
    for keys in [[DEAD_LETTERED, "x"], ["x", DEAD_LETTERED]] {
        assert_eq!(
            Infrastructure::read(internal, &keys),
            Some(Infrastructure::Parked)
        );
    }
}

#[test]
fn test_a_dead_letter_key_is_parked_only_under_internal() {
    // Under another status the key is no dead letter: not found with it is no
    // infrastructure error this root names.
    assert_eq!(
        Infrastructure::read(Code::NotFound.number(), &[DEAD_LETTERED]),
        None
    );
}

#[test]
fn test_the_no_such_session_key_is_read_under_any_status_but_the_deadline() {
    // The key decides (as the sink read it before this root): a fact recorded
    // nowhere is that, whatever status carried it; a deadline that passed is
    // a deadline that passed.
    for code in [Code::NotFound, Code::Internal, Code::FailedPrecondition] {
        assert_eq!(
            Infrastructure::read(code.number(), &[NO_SUCH_SESSION]),
            Some(Infrastructure::SessionUnknown),
            "{code:?}"
        );
    }
    assert_eq!(
        Infrastructure::read(Code::DeadlineExceeded.number(), &[NO_SUCH_SESSION]),
        Some(Infrastructure::TimedOut)
    );
}

#[test]
fn test_a_status_no_infrastructure_error_travels_with_reads_as_none() {
    // A business refusal's statuses (and OK, CANCELLED, a number that is no
    // status) are not this tree's.
    for code in [
        Code::NotFound,
        Code::AlreadyExists,
        Code::PermissionDenied,
        Code::FailedPrecondition,
        Code::OutOfRange,
    ] {
        assert_eq!(Infrastructure::read(code.number(), &[]), None, "{code:?}");
    }
    for number in [0, 1, 17, -1] {
        assert_eq!(Infrastructure::read(number, &[]), None, "{number}");
    }
}

#[test]
fn test_an_unkeyed_status_reads_as_its_unkeyed_error_whatever_other_text_it_carries() {
    assert_eq!(
        Infrastructure::read(Code::Internal.number(), &["the store broke", ""]),
        Some(Infrastructure::Failed)
    );
    assert_eq!(
        Infrastructure::read(Code::Aborted.number(), &["conflict"]),
        Some(Infrastructure::Conflict)
    );
}

#[test]
fn test_a_diagnosed_key_is_unmet_only_under_failed_precondition() {
    // Owner ruling 2026-11-17: a host precondition unmet (git missing or
    // too old) travels failed-precondition, told apart from a business
    // refusal of that status by its key; under another status the key
    // names no such error.
    assert_eq!(
        Infrastructure::read(Code::FailedPrecondition.number(), &["", DIAGNOSED]),
        Some(Infrastructure::Unmet)
    );
    assert_eq!(
        Infrastructure::read(Code::NotFound.number(), &[DIAGNOSED]),
        None
    );
    assert_eq!(
        Infrastructure::read(Code::FailedPrecondition.number(), &[]),
        None
    );
}
