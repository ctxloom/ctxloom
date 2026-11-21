//! The wire messages carry the names the kernel routes by: a stored event's
//! type URL must never change.

use super::v1;
use prost::Name;

#[test]
fn every_message_is_in_the_library_package() {
    for url in [
        v1::LibraryState::type_url(),
        v1::RecordTreeObserved::type_url(),
        v1::RecordStepDone::type_url(),
        v1::RecordStepFailed::type_url(),
        v1::ReplaceFile::type_url(),
        v1::CreateDirectory::type_url(),
        v1::TrashPath::type_url(),
        v1::MovePath::type_url(),
        v1::RunSequence::type_url(),
        v1::TreeObserved::type_url(),
        v1::ChangeIntended::type_url(),
        v1::StepApplied::type_url(),
        v1::ChangeFailed::type_url(),
        v1::IntentSuperseded::type_url(),
        v1::PlanStale::type_url(),
        v1::BuiltinReadOnly::type_url(),
    ] {
        assert!(url.starts_with("/ctxloom.library.v1."), "{url}");
    }
}

#[test]
fn a_steps_arms_are_the_field_numbers_a_oneof_would_give_them() {
    use prost::Message;
    // `Step` and `TreeObserved`'s cause are optional fields, not a oneof
    // (the domain layer maps none yet): one arm set encodes as that field
    // alone, as a oneof's arm does, so a later oneof reads it.
    let step = v1::Step {
        trash: Some(v1::TrashPath {
            path: "p".to_string(),
            ..v1::TrashPath::default()
        }),
        ..v1::Step::default()
    };
    let bytes = step.encode_to_vec();
    assert_eq!(bytes[0], (3 << 3) | 2, "field 3, length-delimited");
    let observed = v1::TreeObserved {
        intended: Some("r-1".to_string()),
        ..v1::TreeObserved::default()
    };
    assert_eq!(observed.encode_to_vec()[0], (3 << 3) | 2);
}
