use super::Held;

// Defect 1: a held session lock renders as held, and nothing of its guard.
#[test]
fn a_held_session_lock_renders_as_held() {
    assert_eq!(format!("{:?}", Held::new(42_u8)), "Held");
}
