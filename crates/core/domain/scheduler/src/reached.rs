//! `RecordAlarmReached`, the time fact a target ingests. Written by hand: the
//! markup layer generates domain types only for the messages a component
//! declares, and this one belongs to no scheduler component but to every
//! target's (`#[handles_fact(RecordAlarmReached)]`).

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::{Timestamp, WireError};
use scheduler_proto::v1 as wire;

/// `at` is reached for the alarm the target armed as `alarm_id`. `at` is the
/// instant armed, not the clock's reading when it fired.
#[derive(Clone, Debug, PartialEq)]
pub struct RecordAlarmReached {
    pub alarm_id: String,
    pub at: Timestamp,
}

impl DomainMessage for RecordAlarmReached {
    type Wire = wire::RecordAlarmReached;

    fn to_wire(&self) -> wire::RecordAlarmReached {
        wire::RecordAlarmReached {
            alarm_id: self.alarm_id.clone(),
            at: Some(self.at),
        }
    }

    fn from_wire(wire: wire::RecordAlarmReached) -> Result<Self, WireError> {
        Ok(RecordAlarmReached {
            alarm_id: wire.alarm_id,
            at: wire.at.ok_or(WireError::Missing {
                message: "io.angzarr.scheduler.v1.RecordAlarmReached",
                field: "at",
            })?,
        })
    }
}

#[cfg(test)]
#[path = "reached.test.rs"]
mod tests;
