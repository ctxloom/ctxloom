use angzarr_diagnostic::Diagnostic;

use super::*;

#[test]
fn test_a_whole_stamp_passes() {
    assert_eq!(checked("v0.1.0-abc1234").unwrap(), "v0.1.0-abc1234");
    let dirty = "v0.1.0-abc1234-20261111T043015-dirty";
    assert_eq!(checked(dirty).unwrap(), dirty);
}

#[test]
fn test_an_empty_or_bare_version_is_refused_naming_it() {
    for version in ["", "0.1.0"] {
        let refused = checked(version).unwrap_err();
        assert_eq!(refused.code(), "ctxloom-unstamped");
        let args = refused.args();
        assert_eq!(args.len(), 1);
        assert_eq!(args[0].0, "stamp");
        match &args[0].1 {
            angzarr_diagnostic::Arg::Str(stamp) => assert_eq!(stamp, version),
            other => panic!("the stamp is a string: {other:?}"),
        }
    }
}
