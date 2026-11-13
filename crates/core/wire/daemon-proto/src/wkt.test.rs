//! The well-known types' proto3 JSON.

use std::collections::BTreeMap;

use crate::google::protobuf::{value::Kind, FieldMask, ListValue, Struct, Timestamp, Value};

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

fn number(n: f64) -> Value {
    Value {
        kind: Some(Kind::NumberValue(n)),
    }
}

#[test]
fn test_a_struct_is_the_object_itself_its_keys_in_order() {
    let mut fields = BTreeMap::new();
    fields.insert(
        "defaultMode".to_string(),
        Value {
            kind: Some(Kind::StringValue("acceptEdits".to_string())),
        },
    );
    fields.insert(
        "allow".to_string(),
        Value {
            kind: Some(Kind::ListValue(ListValue {
                values: vec![
                    Value {
                        kind: Some(Kind::BoolValue(true)),
                    },
                    number(1.0),
                    number(1.5),
                    Value { kind: None },
                ],
            })),
        },
    );
    let document = Struct { fields };
    assert_eq!(
        json(&document),
        r#"{"allow":[true,1,1.5,null],"defaultMode":"acceptEdits"}"#
    );
    let read: Struct = serde_json::from_str(&json(&document)).expect("reads");
    assert_eq!(json(&read), json(&document));
    assert!(serde_json::from_str::<Struct>("[1]").is_err());
    assert!(serde_json::from_str::<ListValue>("{}").is_err());
}

#[test]
fn test_a_number_no_double_holds_as_json_is_its_word() {
    assert_eq!(json(&number(f64::NAN)), r#""NaN""#);
    assert_eq!(json(&number(f64::INFINITY)), r#""Infinity""#);
    assert_eq!(json(&number(f64::NEG_INFINITY)), r#""-Infinity""#);
    assert_eq!(json(&number(-3.0)), "-3");
    assert_eq!(json(&number(1e300)), "1e+300");
}

// A free-form document is a `Value` (OQ-11): any JSON value reads in and
// is written back as it was, its numbers read under serde_json's
// `arbitrary_precision` (as the binary builds it).
#[test]
fn test_a_value_reads_back_any_json_value_it_wrote() {
    for written in [
        r#"{"allow":["Bash",2,1.5],"mode":"plan","none":null,"on":true}"#,
        r#"["Bash",{"deny":"WebFetch"},-3]"#,
        r#""plan""#,
        "7",
        "1.5",
        "false",
        "null",
    ] {
        let read: Value = serde_json::from_str(written).expect("reads");
        assert_eq!(json(&read), written);
    }
}
