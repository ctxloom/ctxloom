//! The posture table: nothing is claimed enforced that the launch does not do.

use super::*;

// Ruling 109: the host enforces neither network nor sandbox, and no container
// runtime does yet either (a run on one is still launched on the host).
#[test]
fn no_runtime_enforces_a_restrictive_posture_yet() {
    for runtime in ["host", "container-rootless", "container-rootful", ""] {
        assert_eq!(enforced(runtime), Enforced::default(), "{runtime}");
    }
}
