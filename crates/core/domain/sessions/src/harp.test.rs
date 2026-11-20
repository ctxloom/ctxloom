//! What a harp may be called. Go: `internal/shared/harp` (`Validate`,
//! release/0.7; its `ValidateRename` bound is gone with the rename, S55).

use crate::{validate, SessionRejection};

fn invalid(harp: &str, reason: &str) -> SessionRejection {
    SessionRejection::HarpInvalid {
        harp: harp.to_string(),
        reason: reason.to_string(),
    }
}

// Go: Validate rejects only what cannot be a path component: it is
// "deliberately permissive on charset".
#[test]
fn validate_refuses_what_cannot_be_a_path_component() {
    assert_eq!(validate(""), Err(invalid("", "empty")));
    assert_eq!(validate("."), Err(invalid(".", "directory")));
    assert_eq!(validate(".."), Err(invalid("..", "directory")));
    for bad in ["a/b", "a\\b", "a:b"] {
        assert_eq!(validate(bad), Err(invalid(bad, "separator")), "{bad:?}");
    }
    for bad in ["nul\0name", "tab\tname", "del\x7fname", "line\nbreak"] {
        assert_eq!(validate(bad), Err(invalid(bad, "control")), "{bad:?}");
    }
    for bad in [" lead", "trail ", "\u{a0}x"] {
        // U+00A0 is whitespace to Go's TrimSpace.
        assert_eq!(validate(bad), Err(invalid(bad, "whitespace")), "{bad:?}");
    }
}

#[test]
fn validate_accepts_harps_and_the_odd_name_a_person_might_choose() {
    for good in [
        "amber-swift-owl",
        "x",
        "\u{dc}n\u{ef}code-n\u{e4}me",
        "with space inside",
        "dots.ok",
    ] {
        assert_eq!(validate(good), Ok(()), "{good:?}");
    }
}
