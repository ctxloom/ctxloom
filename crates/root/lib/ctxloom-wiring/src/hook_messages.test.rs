//! A hook's one call reaches its daemon trusting the discovery file first
//! (the lean hook RPC), and probes only once that daemon is unreachable.

use std::sync::Arc;
use std::time::{Duration, Instant};

use daemon_ports::Daemons;

use crate::hook_library::HookDaemons;

// Its first reach is the discovery file's daemon, unprobed; a second (the
// first answered unreachable) probes, and a daemon that is not live, with
// none to be started, is not reached.
#[test]
fn test_a_hooks_first_reach_trusts_the_discovery_file_and_a_second_probes() {
    let world = crate::commands::tests::World::new("hook-trusting");
    let wiring = world.wiring();
    let home = wiring.home().display().to_string();
    crate::daemon::publish_for_test(
        &wiring,
        &run_ports::Discovery {
            stamp: "v0.1.0-abc1234".to_string(),
            pid: 7,
            endpoint: "https://127.0.0.1:1".to_string(),
            cert: run_ports::PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
            bearer: run_ports::Token::from_bytes([7; 32]),
            home,
        },
    );
    let daemons = HookDaemons::trusting(
        Arc::clone(wiring.env()),
        "v1.2.3-abc",
        Instant::now() + Duration::from_secs(2),
        false,
    );
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    runtime.block_on(async {
        assert!(daemons.reach().await.is_ok(), "trusted, unprobed");
        assert!(daemons.reach().await.is_err(), "probed: not live");
    });
}
