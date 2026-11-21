//! The catalog's distilled states (OD-40, B1, T30, S51).

use super::*;

// S51, T30: a distilled form's state is the API's, and `bundle show`'s stale
// one is STALE whatever else it says.
#[test]
fn test_a_distilled_form_s_state_is_the_api_s() {
    assert_eq!(distilled(true), v1::DistilledState::Fresh as i32);
    assert_eq!(distilled(false), v1::DistilledState::None as i32);
    assert_eq!(
        shown_distilled(true, true),
        v1::DistilledState::Stale as i32
    );
    assert_eq!(
        shown_distilled(true, false),
        v1::DistilledState::Fresh as i32
    );
    assert_eq!(
        shown_distilled(false, false),
        v1::DistilledState::None as i32
    );
}
