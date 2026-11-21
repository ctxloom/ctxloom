//! The stamp's semver precedence, moved here from daemon-api with `newer`.

use super::newer;

// "Newer" is the VERSION's semver precedence alone (the hash and a dirty
// build's time order nothing): its numbers, then a release above its
// pre-releases, which compare identifier by identifier (numbers by value
// and below words; a longer list above its prefix).
#[test]
fn newer_is_the_versions_semver_precedence() {
    for (own, daemon) in [
        ("v0.2.0-aaaaaaa", "v0.1.0-bbbbbbb"),
        ("v1.0.0-aaaaaaa", "v0.9.9-bbbbbbb"),
        ("v0.1.10-aaaaaaa", "v0.1.9-bbbbbbb"),
        ("v0.10.0-aaaaaaa", "v0.9.0-bbbbbbb"),
        ("v1.0.0-aaaaaaa", "v1.0.0-rc.1-bbbbbbb"),
        ("v1.0.0-rc.2-aaaaaaa", "v1.0.0-rc.1-bbbbbbb"),
        ("v1.0.0-rc.10-aaaaaaa", "v1.0.0-rc.9-bbbbbbb"),
        ("v1.0.0-rc-aaaaaaa", "v1.0.0-9-bbbbbbb"),
        ("v1.0.0-rc.1.1-aaaaaaa", "v1.0.0-rc.1-bbbbbbb"),
        ("v0.2.0-aaaaaaa-20261114T101010-dirty", "v0.1.0-bbbbbbb"),
        ("v0.2.0-aaaaaaa", "v0.1.0-bbbbbbb-20261114T101010-dirty"),
    ] {
        assert!(newer(own, daemon), "{own} is newer than {daemon}");
        assert!(!newer(daemon, own), "{daemon} is not newer than {own}");
    }
    for (a, b) in [
        ("v0.2.0-aaaaaaa", "v0.2.0-bbbbbbb"),
        (
            "v0.2.0-aaaaaaa-20261114T101010-dirty",
            "v0.2.0-aaaaaaa-20261115T101010-dirty",
        ),
        ("v0.2.0-aaaaaaa", "0.2.0-aaaaaaa"),
        ("v0.2.0-aaaaaaa", "v0.2-aaaaaaa"),
        ("v0.2.0", "v0.1.0-aaaaaaa"),
        ("", "v0.1.0-aaaaaaa"),
        ("v0.2.0-", "v0.1.0-aaaaaaa"),
        ("v0.2.0-zzzzzzz", "v0.1.0-aaaaaaa"),
    ] {
        assert!(!newer(a, b), "{a} vs {b}");
        assert!(!newer(b, a), "{b} vs {a}");
    }
}
