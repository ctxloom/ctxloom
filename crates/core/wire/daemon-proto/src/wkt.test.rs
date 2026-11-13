//! The well-known types' proto3 JSON.

use crate::google::protobuf::{FieldMask, Timestamp};

fn json<T: serde::Serialize>(value: &T) -> String {
    serde_json::to_string(value).expect("serializes")
}

#[test]
fn test_a_timestamp_is_rfc3339_in_utc_with_a_fraction_of_0_3_6_or_9_digits() {
    let at = |nanos| Timestamp {
        seconds: 1_784_309_252,
        nanos,
    };
    assert_eq!(json(&at(0)), r#""2026-07-17T17:27:32Z""#);
    assert_eq!(json(&at(481_000_000)), r#""2026-07-17T17:27:32.481Z""#);
    assert_eq!(json(&at(481_200_000)), r#""2026-07-17T17:27:32.481200Z""#);
    assert_eq!(
        json(&at(481_200_007)),
        r#""2026-07-17T17:27:32.481200007Z""#
    );
}

#[test]
fn test_a_timestamp_reads_back_what_it_wrote_and_an_offset_as_utc() {
    let at = Timestamp {
        seconds: 1_784_309_252,
        nanos: 481_000_000,
    };
    let read: Timestamp = serde_json::from_str(&json(&at)).expect("reads");
    assert_eq!(read, at);
    let offset: Timestamp = serde_json::from_str(r#""2026-07-17T19:27:32+02:00""#).expect("reads");
    assert_eq!(offset.seconds, 1_784_309_252);
    assert!(serde_json::from_str::<Timestamp>(r#""yesterday""#).is_err());
}

#[test]
fn test_a_timestamp_converts_to_and_from_prost_types() {
    let ours = Timestamp {
        seconds: 7,
        nanos: 9,
    };
    let theirs: prost_types::Timestamp = ours.into();
    assert_eq!((theirs.seconds, theirs.nanos), (7, 9));
    assert_eq!(Timestamp::from(theirs), ours);
}

#[test]
fn test_a_field_mask_is_its_camel_case_paths_joined() {
    let mask = FieldMask {
        paths: vec!["session_home".to_string(), "env".to_string()],
    };
    assert_eq!(json(&mask), r#""sessionHome,env""#);
    let read: FieldMask = serde_json::from_str(r#""sessionHome,env""#).expect("reads");
    assert_eq!(read, mask);
    let empty: FieldMask = serde_json::from_str(r#""""#).expect("reads");
    assert!(empty.paths.is_empty());
}
