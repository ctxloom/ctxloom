//! Where the run's hook listener binds and what a child is told (owner
//! ruling 2026-11-09, item 33): the narrowest bind the child can reach, per
//! runtime, with the bind address and the address told to the child kept
//! apart. Pure: the platform's facts (a bridge's gateway, the result of a
//! probe from inside the child) are injected, never read here.
//!
//! The rows:
//!
//! | runtime                     | bind                | told                        |
//! |-----------------------------|---------------------|-----------------------------|
//! | the host (interactive)      | 127.0.0.1           | 127.0.0.1                   |
//! | docker, rootful Linux       | the per-run bridge's gateway IP | `host.docker.internal` |
//! | podman, rootless (pasta -T) | 127.0.0.1           | `host.containers.internal`  |
//! | Docker Desktop              | 127.0.0.1           | `host.docker.internal`      |
//! | Colima, Lima                | 127.0.0.1           | `host.lima.internal`        |
//!
//! No plan binds an unspecified address, and a loopback bind a probe found
//! unreachable is a refusal with its fix, never a wider bind. Only the host
//! row is wired to a launch; the container rows are the table the child
//! hooks landing uses.

use std::fmt;
use std::net::{IpAddr, Ipv4Addr};

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::Arrival;

/// A container runtime the table knows.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ContainerRuntime {
    DockerRootful,
    DockerDesktop,
    Colima,
    Lima,
    PodmanRootless,
}

impl ContainerRuntime {
    /// The runtime's name, as a refusal says it.
    pub fn name(self) -> &'static str {
        match self {
            ContainerRuntime::DockerRootful => "docker",
            ContainerRuntime::DockerDesktop => "docker-desktop",
            ContainerRuntime::Colima => "colima",
            ContainerRuntime::Lima => "lima",
            ContainerRuntime::PodmanRootless => "podman",
        }
    }
}

/// Where the run's engine runs.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Runtime {
    /// A process of the host (the interactive engine).
    Host,
    Container(ContainerRuntime),
}

/// What a probe from inside the child found of the host's loopback.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Probe {
    NotRun,
    Reachable,
    Unreachable,
}

/// The platform's facts the table needs.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PlatformFacts {
    /// The gateway IP of the run's user-defined bridge (rootful docker).
    pub bridge_gateway: Option<IpAddr>,
    /// The probe of a loopback bind, from inside the child.
    pub loopback_probe: Probe,
}

impl PlatformFacts {
    /// No facts: no bridge, no probe run.
    pub const NONE: PlatformFacts = PlatformFacts {
        bridge_gateway: None,
        loopback_probe: Probe::NotRun,
    };
}

/// Where to bind and what to tell.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BindPlan {
    /// The address the listener binds (port 0: the system gives one).
    pub bind_addr: IpAddr,
    /// The host the child is told to dial: an IP literal equal to the bind, or
    /// the runtime's alias for the host.
    pub told_host: String,
    /// What the listener declares of its requests.
    pub arrival: Arrival,
    /// The bound port must be forwarded into the child (podman's `pasta -T`).
    pub forward_port: bool,
}

/// Why there is no plan.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BindRefusal {
    /// A probe from inside the child found the host's loopback unreachable.
    LoopbackUnreachable { runtime: &'static str },
    /// The runtime needs its bridge's gateway and was given none.
    NoBridgeGateway { runtime: &'static str },
    /// A plan binds an unspecified address (0.0.0.0, ::).
    UnspecifiedBind { addr: IpAddr },
    /// The told host is an IP other than the bind: the token would be
    /// published to an interface the listener is not on.
    ToldOtherThanBound { bind: IpAddr, told: String },
}

fn loopback() -> IpAddr {
    IpAddr::V4(Ipv4Addr::LOCALHOST)
}

/// The plan for `runtime` on a platform with `facts`.
pub fn bind_for(runtime: Runtime, facts: &PlatformFacts) -> Result<BindPlan, BindRefusal> {
    let container = match runtime {
        Runtime::Host => return Ok(on_loopback("127.0.0.1", false)),
        Runtime::Container(container) => container,
    };
    let name = container.name();
    match (container, facts.bridge_gateway) {
        (ContainerRuntime::DockerRootful, gateway) => {
            let gateway = gateway.ok_or(BindRefusal::NoBridgeGateway { runtime: name })?;
            let plan = BindPlan {
                bind_addr: gateway,
                told_host: "host.docker.internal".to_string(),
                arrival: arrival_of(gateway),
                forward_port: false,
            };
            plan.check().map(|()| plan)
        }
        _ if facts.loopback_probe == Probe::Unreachable => {
            Err(BindRefusal::LoopbackUnreachable { runtime: name })
        }
        (ContainerRuntime::PodmanRootless, _) => Ok(on_loopback("host.containers.internal", true)),
        (ContainerRuntime::DockerDesktop, _) => Ok(on_loopback("host.docker.internal", false)),
        (ContainerRuntime::Colima | ContainerRuntime::Lima, _) => {
            Ok(on_loopback("host.lima.internal", false))
        }
    }
}

fn arrival_of(addr: IpAddr) -> Arrival {
    match addr.is_loopback() {
        true => Arrival::Loopback,
        false => Arrival::Ip,
    }
}

fn on_loopback(told: &str, forward_port: bool) -> BindPlan {
    BindPlan {
        bind_addr: loopback(),
        told_host: told.to_string(),
        arrival: Arrival::Loopback,
        forward_port,
    }
}

impl BindPlan {
    /// The invariants of every plan: nothing unspecified, and an IP told is
    /// the IP bound.
    pub fn check(&self) -> Result<(), BindRefusal> {
        if self.bind_addr.is_unspecified() {
            return Err(BindRefusal::UnspecifiedBind {
                addr: self.bind_addr,
            });
        }
        match self.told_host.parse::<IpAddr>() {
            Ok(told) if told != self.bind_addr => Err(BindRefusal::ToldOtherThanBound {
                bind: self.bind_addr,
                told: self.told_host.clone(),
            }),
            _ => Ok(()),
        }
    }
}

impl fmt::Display for BindRefusal {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for BindRefusal {
    fn code(&self) -> &str {
        match self {
            BindRefusal::LoopbackUnreachable { .. } => code!("bind-loopback-unreachable"),
            BindRefusal::NoBridgeGateway { .. } => code!("bind-no-bridge-gateway"),
            BindRefusal::UnspecifiedBind { .. } => code!("bind-unspecified"),
            BindRefusal::ToldOtherThanBound { .. } => code!("bind-told-other-than-bound"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            BindRefusal::LoopbackUnreachable { runtime }
            | BindRefusal::NoBridgeGateway { runtime } => vec![("runtime", (*runtime).into())],
            BindRefusal::UnspecifiedBind { addr } => vec![("addr", addr.to_string().into())],
            BindRefusal::ToldOtherThanBound { bind, told } => vec![
                ("bind", bind.to_string().into()),
                ("told", told.as_str().into()),
            ],
        }
    }
}

/// One value of every refusal, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(BindRefusal::LoopbackUnreachable { runtime: "colima" }),
        Box::new(BindRefusal::NoBridgeGateway { runtime: "docker" }),
        Box::new(BindRefusal::UnspecifiedBind {
            addr: IpAddr::V4(Ipv4Addr::UNSPECIFIED),
        }),
        Box::new(BindRefusal::ToldOtherThanBound {
            bind: loopback(),
            told: "192.168.1.5".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "bind.test.rs"]
mod tests;
