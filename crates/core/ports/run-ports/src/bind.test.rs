use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use angzarr_diagnostic::Diagnostic;

use crate::bind::{
    bind_for, BindPlan, BindRefusal, ContainerRuntime as C, PlatformFacts, Probe, Runtime,
};
use crate::Arrival;

const LOOPBACK: IpAddr = IpAddr::V4(Ipv4Addr::LOCALHOST);
const GATEWAY: IpAddr = IpAddr::V4(Ipv4Addr::new(172, 18, 0, 1));

fn facts(gateway: Option<IpAddr>, probe: Probe) -> PlatformFacts {
    PlatformFacts {
        bridge_gateway: gateway,
        loopback_probe: probe,
    }
}

fn plan(bind_addr: IpAddr, told: &str, arrival: Arrival, forward_port: bool) -> BindPlan {
    BindPlan {
        bind_addr,
        told_host: told.to_string(),
        arrival,
        forward_port,
    }
}

#[test]
fn the_host_binds_loopback_and_tells_loopback() {
    assert_eq!(
        bind_for(Runtime::Host, &PlatformFacts::NONE).unwrap(),
        plan(LOOPBACK, "127.0.0.1", Arrival::Loopback, false)
    );
}

// Ruling 33's table, row by row: bind and told kept apart.
#[test]
fn each_container_row_binds_the_narrowest_and_tells_the_runtimes_alias() {
    let rows = [
        (
            C::DockerRootful,
            facts(Some(GATEWAY), Probe::NotRun),
            plan(GATEWAY, "host.docker.internal", Arrival::Ip, false),
        ),
        (
            C::PodmanRootless,
            facts(None, Probe::NotRun),
            plan(
                LOOPBACK,
                "host.containers.internal",
                Arrival::Loopback,
                true,
            ),
        ),
        (
            C::DockerDesktop,
            facts(None, Probe::Reachable),
            plan(LOOPBACK, "host.docker.internal", Arrival::Loopback, false),
        ),
        (
            C::Colima,
            facts(None, Probe::NotRun),
            plan(LOOPBACK, "host.lima.internal", Arrival::Loopback, false),
        ),
        (
            C::Lima,
            facts(None, Probe::Reachable),
            plan(LOOPBACK, "host.lima.internal", Arrival::Loopback, false),
        ),
    ];
    for (runtime, facts, expected) in rows {
        assert_eq!(
            bind_for(Runtime::Container(runtime), &facts).unwrap(),
            expected,
            "{runtime:?}"
        );
    }
}

#[test]
fn a_loopback_bind_the_probe_found_unreachable_is_a_refusal_never_a_wider_bind() {
    for runtime in [C::PodmanRootless, C::DockerDesktop, C::Colima, C::Lima] {
        let refused = bind_for(
            Runtime::Container(runtime),
            &facts(Some(GATEWAY), Probe::Unreachable),
        )
        .unwrap_err();
        assert_eq!(
            refused,
            BindRefusal::LoopbackUnreachable {
                runtime: runtime.name()
            }
        );
        assert_eq!(refused.code(), "run-ports-bind-loopback-unreachable");
    }
}

#[test]
fn rootful_docker_needs_its_bridge_gateway_and_never_an_unspecified_one() {
    assert_eq!(
        bind_for(Runtime::Container(C::DockerRootful), &PlatformFacts::NONE).unwrap_err(),
        BindRefusal::NoBridgeGateway { runtime: "docker" }
    );
    for unspecified in [
        IpAddr::V4(Ipv4Addr::UNSPECIFIED),
        IpAddr::V6(Ipv6Addr::UNSPECIFIED),
    ] {
        assert_eq!(
            bind_for(
                Runtime::Container(C::DockerRootful),
                &facts(Some(unspecified), Probe::NotRun)
            )
            .unwrap_err(),
            BindRefusal::UnspecifiedBind { addr: unspecified }
        );
    }
}

// The property: whatever the facts, no plan binds an unspecified address, and
// the arrival declared is Loopback only for a loopback bind.
#[test]
fn no_plan_binds_an_unspecified_address() {
    let gateways = [
        None,
        Some(GATEWAY),
        Some(LOOPBACK),
        Some(IpAddr::V4(Ipv4Addr::UNSPECIFIED)),
        Some(IpAddr::V6(Ipv6Addr::UNSPECIFIED)),
    ];
    let runtimes = [
        Runtime::Host,
        Runtime::Container(C::DockerRootful),
        Runtime::Container(C::DockerDesktop),
        Runtime::Container(C::Colima),
        Runtime::Container(C::Lima),
        Runtime::Container(C::PodmanRootless),
    ];
    for runtime in runtimes {
        for gateway in gateways {
            for probe in [Probe::NotRun, Probe::Reachable, Probe::Unreachable] {
                if let Ok(plan) = bind_for(runtime, &facts(gateway, probe)) {
                    assert!(!plan.bind_addr.is_unspecified(), "{runtime:?} {gateway:?}");
                    assert_eq!(
                        plan.arrival == Arrival::Loopback,
                        plan.bind_addr.is_loopback()
                    );
                    assert_eq!(plan.check(), Ok(()));
                }
            }
        }
    }
}

// Ruling 4 of the owner's additions: a told IP other than the bind would
// publish the token toward an interface the listener is not on.
#[test]
fn a_told_ip_other_than_the_bound_one_is_refused() {
    let wider = plan(LOOPBACK, "192.168.1.5", Arrival::Loopback, false);
    assert_eq!(
        wider.check().unwrap_err(),
        BindRefusal::ToldOtherThanBound {
            bind: LOOPBACK,
            told: "192.168.1.5".to_string()
        }
    );
    let unspecified_told = plan(GATEWAY, "0.0.0.0", Arrival::Ip, false);
    assert!(unspecified_told.check().is_err());
    let v6 = plan(LOOPBACK, "::1", Arrival::Loopback, false);
    assert!(v6.check().is_err());
    // The same IP, or a name (the runtime's alias), is fine.
    assert_eq!(
        plan(GATEWAY, "172.18.0.1", Arrival::Ip, false).check(),
        Ok(())
    );
    assert_eq!(
        plan(LOOPBACK, "host.lima.internal", Arrival::Loopback, false).check(),
        Ok(())
    );
}

#[test]
fn a_plan_that_binds_unspecified_fails_its_check() {
    let bad = plan(
        IpAddr::V4(Ipv4Addr::UNSPECIFIED),
        "host.docker.internal",
        Arrival::Ip,
        false,
    );
    assert_eq!(
        bad.check().unwrap_err(),
        BindRefusal::UnspecifiedBind {
            addr: IpAddr::V4(Ipv4Addr::UNSPECIFIED)
        }
    );
}

#[test]
fn the_refusals_display_their_args_exactly() {
    assert_eq!(
        BindRefusal::LoopbackUnreachable { runtime: "colima" }.to_string(),
        "run-ports-bind-loopback-unreachable runtime=colima"
    );
    assert_eq!(
        BindRefusal::NoBridgeGateway { runtime: "docker" }.to_string(),
        "run-ports-bind-no-bridge-gateway runtime=docker"
    );
    assert_eq!(
        BindRefusal::UnspecifiedBind {
            addr: IpAddr::V4(Ipv4Addr::UNSPECIFIED)
        }
        .to_string(),
        "run-ports-bind-unspecified addr=0.0.0.0"
    );
    assert_eq!(
        BindRefusal::ToldOtherThanBound {
            bind: LOOPBACK,
            told: "10.0.0.1".to_string()
        }
        .to_string(),
        "run-ports-bind-told-other-than-bound bind=127.0.0.1 told=10.0.0.1"
    );
}
