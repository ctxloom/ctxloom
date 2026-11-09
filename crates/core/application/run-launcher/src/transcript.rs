//! The canonical transcript's lines (D27): the envelope `v, harp, session_id,
//! engine, seq, ts, kind` and the entry, one JSON object per line in
//! `transcripts/transcript.jsonl`. Pure: the instant and the sequence enter as
//! arguments.

/// The transcript's path in a session's directory.
pub const TRANSCRIPT: &str = "transcripts/transcript.jsonl";

/// Who spoke an entry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Speaker {
    User,
    Assistant,
}

impl Speaker {
    fn name(self) -> &'static str {
        match self {
            Speaker::User => "user",
            Speaker::Assistant => "assistant",
        }
    }
}

/// One entry's line, newline included. `seq` is per transcript.
pub fn entry_line(
    harp: &str,
    session_id: &str,
    engine: &str,
    seq: u64,
    seconds: i64,
    speaker: Speaker,
    content: &str,
) -> String {
    let line = serde_json::json!({
        "v": 1,
        "harp": harp,
        "session_id": session_id,
        "engine": engine,
        "seq": seq,
        "ts": rfc3339(seconds),
        "kind": "entry",
        "entry": {"type": speaker.name(), "content": content},
    });
    format!("{line}\n")
}

/// The sequence the next line of `transcript` takes: how many lines it holds.
pub fn next_seq(transcript: &[u8]) -> u64 {
    transcript
        .split(|byte| *byte == b'\n')
        .filter(|line| !line.iter().all(u8::is_ascii_whitespace))
        .count() as u64
}

/// `seconds` since the epoch as an RFC 3339 instant in UTC (Hinnant's civil
/// from days).
pub fn rfc3339(seconds: i64) -> String {
    let days = seconds.div_euclid(86_400);
    let of_day = seconds.rem_euclid(86_400);
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let day_of_era = z.rem_euclid(146_097);
    let year_of_era =
        (day_of_era - day_of_era / 1_460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let shifted_month = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * shifted_month + 2) / 5 + 1;
    let month = if shifted_month < 10 {
        shifted_month + 3
    } else {
        shifted_month - 9
    };
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        of_day / 3_600,
        of_day % 3_600 / 60,
        of_day % 60
    )
}

#[cfg(test)]
#[path = "transcript.test.rs"]
mod tests;
