//! The launch on the wire (T10): `run_ports::Launch`, the one launch's
//! value, as `ctxloom.v1.Launch` and back. The value is the core's
//! (`run-ports`); only this layer sees the wire type.
//!
//! What the launch said (its ignored permits, the settings it left out) and
//! its input (a one-shot turn's prompt) are never on the wire: a launch read
//! from it says nothing and feeds nothing. A one-shot's piped stdio has no
//! wire value of its own (`STDIO_UNSPECIFIED`): its launch is the daemon's
//! and never crosses.

use std::time::Duration;

use daemon_proto::v1;
use run_ports::{Launch, LaunchAt, LaunchKind, Stdio, StopPolicy};

/// `launch` as the wire carries it.
pub fn to_wire(launch: &Launch) -> v1::Launch {
    v1::Launch {
        at: Some(v1::LaunchAt {
            project_id: launch.at.project_id.clone(),
            session: launch.at.session.clone(),
            harp: launch.at.harp.clone(),
            run: launch.at.run.clone(),
            incarnation: launch.at.incarnation,
        }),
        kind: kind_to_wire(launch.kind) as i32,
        engine: launch.engine.clone(),
        program: launch.program.clone(),
        args: launch.args.clone(),
        // A BTreeMap: sorted by name, as the wire says.
        env: launch
            .env
            .iter()
            .map(|(name, value)| v1::EnvVar {
                name: name.clone(),
                value: value.clone(),
            })
            .collect(),
        cwd: launch.cwd.clone().unwrap_or_default(),
        session_home: launch.session_home.clone(),
        session_lock: launch.session_lock.clone(),
        stdio: stdio_to_wire(launch.stdio) as i32,
        stop: Some(v1::StopPolicy {
            signal: launch.stop.signal,
            grace_ms: u32::try_from(launch.stop.grace.as_millis()).unwrap_or(u32::MAX),
            close_stdin_first: launch.stop.close_stdin_first,
        }),
    }
}

/// The launch `wire` carries; none when it names no kind it knows. A launch
/// with no stop is stopped as its kind is ([`StopPolicy::of`]).
pub fn from_wire(wire: &v1::Launch) -> Option<Launch> {
    let kind = kind_from_wire(v1::launch::Kind::try_from(wire.kind).ok()?)?;
    let stdio = match v1::launch::Stdio::try_from(wire.stdio) {
        Ok(v1::launch::Stdio::Inherit) => Stdio::Inherit,
        Ok(v1::launch::Stdio::StreamJson) => Stdio::StreamJson,
        Ok(v1::launch::Stdio::Acp) => Stdio::Acp,
        Ok(v1::launch::Stdio::Unspecified) | Err(_) => Stdio::Piped,
    };
    let at = wire
        .at
        .as_ref()
        .map_or_else(LaunchAt::default, |at| LaunchAt {
            project_id: at.project_id.clone(),
            session: at.session.clone(),
            harp: at.harp.clone(),
            run: at.run.clone(),
            incarnation: at.incarnation,
        });
    let stop = wire.stop.as_ref().map_or_else(
        || StopPolicy::of(kind),
        |stop| StopPolicy {
            signal: stop.signal,
            grace: Duration::from_millis(u64::from(stop.grace_ms)),
            close_stdin_first: stop.close_stdin_first,
        },
    );
    Some(Launch {
        at,
        kind,
        engine: wire.engine.clone(),
        program: wire.program.clone(),
        args: wire.args.clone(),
        env: wire
            .env
            .iter()
            .map(|var| (var.name.clone(), var.value.clone()))
            .collect(),
        // The input is the host's alone, and what the launch said its
        // maker's: neither crosses.
        stdin: Vec::new(),
        cwd: (!wire.cwd.is_empty()).then(|| wire.cwd.clone()),
        session_home: wire.session_home.clone(),
        session_lock: wire.session_lock.clone(),
        stdio,
        stop,
        ignored: Vec::new(),
        left_out: Vec::new(),
    })
}

fn kind_to_wire(kind: LaunchKind) -> v1::launch::Kind {
    match kind {
        LaunchKind::Terminal => v1::launch::Kind::Terminal,
        LaunchKind::Child => v1::launch::Kind::Child,
        LaunchKind::OneShot => v1::launch::Kind::OneShot,
        LaunchKind::Editor => v1::launch::Kind::Editor,
    }
}

fn kind_from_wire(kind: v1::launch::Kind) -> Option<LaunchKind> {
    match kind {
        v1::launch::Kind::Unspecified => None,
        v1::launch::Kind::Terminal => Some(LaunchKind::Terminal),
        v1::launch::Kind::Child => Some(LaunchKind::Child),
        v1::launch::Kind::OneShot => Some(LaunchKind::OneShot),
        v1::launch::Kind::Editor => Some(LaunchKind::Editor),
    }
}

fn stdio_to_wire(stdio: Stdio) -> v1::launch::Stdio {
    match stdio {
        Stdio::Piped => v1::launch::Stdio::Unspecified,
        Stdio::Inherit => v1::launch::Stdio::Inherit,
        Stdio::StreamJson => v1::launch::Stdio::StreamJson,
        Stdio::Acp => v1::launch::Stdio::Acp,
    }
}

#[cfg(test)]
#[path = "launch.test.rs"]
mod tests;
