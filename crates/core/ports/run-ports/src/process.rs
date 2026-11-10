//! The processes of this host, as a run's owner is judged by (ruling 86): a
//! run's enqueue records the process that will launch it, and a later
//! process that finds the run still owed a runner asks whether that one is
//! still running. Synchronous: a probe of the host, as a lock probe is.

/// A process: its pid and when it started, in the host's own terms (opaque,
/// compared for equality only), so a pid the system has since given another
/// process is not taken for it. All zero is unknown.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ProcessIdentity {
    pub pid: u32,
    pub started: u64,
}

impl ProcessIdentity {
    /// Whether this names no process (a run enqueued before its launcher was
    /// recorded): never judged gone.
    pub fn is_unknown(&self) -> bool {
        self.pid == 0
    }
}

/// What a probe of a process says.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ProcessState {
    /// The process is running: the pid is there, and started when it said.
    Running,
    /// The process has ended: no such pid, or the pid is another process's.
    Gone,
    /// The probe proves nothing (an unknown identity, a host it cannot ask):
    /// never read as gone.
    Unproven,
}

/// The processes of this host.
pub trait Processes: Send + Sync {
    /// This process, as an enqueue records its launcher.
    fn current(&self) -> ProcessIdentity;

    /// Whether the process `who` names is still running.
    fn state(&self, who: &ProcessIdentity) -> ProcessState;
}
