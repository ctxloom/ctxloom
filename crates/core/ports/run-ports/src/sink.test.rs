use super::*;

// Hook capture invariant: a delivery is settled when nothing more can come of
// delivering the same book again; only a failure to reach the handler is not.
#[test]
fn only_a_delivery_that_did_not_reach_the_handler_is_unsettled() {
    for settled in [
        Delivery::Applied,
        Delivery::Duplicate,
        Delivery::Parked,
        Delivery::Refused {
            detail: "not allowlisted".to_string(),
        },
    ] {
        assert!(settled.settled(), "{settled:?}");
    }
    for unsettled in [
        Delivery::Unavailable {
            detail: "connection refused".to_string(),
        },
        Delivery::TimedOut,
    ] {
        assert!(!unsettled.settled(), "{unsettled:?}");
    }
}
