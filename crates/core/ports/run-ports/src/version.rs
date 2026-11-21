//! A versionator stamp's VERSION, as semver orders it: the one "newer"
//! (s4-Q14, H-Q1) that the daemon's skew rule (`daemon_api::reconcile_session`
//! re-exports it) and the hook's hold ([`up_to_date`](crate::up_to_date))
//! both judge by, so no fact is held by one side and refused by the other.

/// Whether stamp `own` names a newer VERSION than stamp `daemon`: semver
/// precedence of the `MAJOR.MINOR.PATCH[-pre]` each stamp
/// (`v<version>-<hash>[-<time>-dirty]`) carries. The hash and a dirty
/// build's time order nothing, so two builds of one version are neither;
/// a stamp not of the shape is never newer, nor older.
pub fn newer(own: &str, daemon: &str) -> bool {
    match (Version::of_stamp(own), Version::of_stamp(daemon)) {
        (Some(own), Some(daemon)) => own.precedes(&daemon) == std::cmp::Ordering::Greater,
        _ => false,
    }
}

/// A stamp's version, as semver orders it.
struct Version<'a> {
    numbers: [u64; 3],
    /// The pre-release identifiers; none, a release.
    pre: Vec<&'a str>,
}

impl<'a> Version<'a> {
    /// The version of `stamp`, or none when it is not a stamp's shape.
    fn of_stamp(stamp: &'a str) -> Option<Version<'a>> {
        let rest = stamp.strip_prefix('v')?;
        let rest = match rest.strip_suffix("-dirty") {
            Some(dirty) => dirty.rsplit_once('-')?.0,
            None => rest,
        };
        let (version, hash) = rest.rsplit_once('-')?;
        if hash.is_empty() || !hash.bytes().all(|b| b.is_ascii_hexdigit()) {
            return None;
        }
        let (core, pre) = match version.split_once('-') {
            Some((core, pre)) => (core, pre.split('.').collect()),
            None => (version, Vec::new()),
        };
        let parsed: Vec<u64> = core
            .split('.')
            .map(|n| {
                n.bytes()
                    .all(|b| b.is_ascii_digit())
                    .then(|| n.parse().ok())
                    .flatten()
            })
            .collect::<Option<_>>()?;
        let numbers: [u64; 3] = parsed.try_into().ok()?;
        if pre.iter().any(|id: &&str| id.is_empty()) {
            return None;
        }
        Some(Version { numbers, pre })
    }

    /// Semver precedence: the numbers; then a release above any of its
    /// pre-releases; then identifier by identifier, numbers by value and
    /// below words, a longer list above its prefix.
    fn precedes(&self, other: &Version<'_>) -> std::cmp::Ordering {
        use std::cmp::Ordering;
        let by_numbers = self.numbers.cmp(&other.numbers);
        if by_numbers != Ordering::Equal {
            return by_numbers;
        }
        match (self.pre.is_empty(), other.pre.is_empty()) {
            (true, true) => return Ordering::Equal,
            (true, false) => return Ordering::Greater,
            (false, true) => return Ordering::Less,
            (false, false) => {}
        }
        for (a, b) in self.pre.iter().zip(&other.pre) {
            let order = match (a.parse::<u64>(), b.parse::<u64>()) {
                (Ok(a), Ok(b)) => a.cmp(&b),
                (Ok(_), Err(_)) => Ordering::Less,
                (Err(_), Ok(_)) => Ordering::Greater,
                (Err(_), Err(_)) => a.cmp(b),
            };
            if order != Ordering::Equal {
                return order;
            }
        }
        self.pre.len().cmp(&other.pre.len())
    }
}

#[cfg(test)]
#[path = "version.test.rs"]
mod tests;
