//! The `Workspaces` port over the git command line (S11). Go:
//! `isolation/worktree.go`, `worktree_reap.go` and `none.go` (release/0.7).
//!
//! The workspace axis: a run asking for a worktree gets a checkout on its
//! session's own branch for its agent (`ctxloom/<harp>/<agent>`), in the
//! session's checkouts directory under ctxloom's home
//! (`<home>/.ctxloom/worktrees/<project-id>/<harp>/<agent>`, owner ruling
//! 2026-11-12), outside the project: an engine there reads no `CLAUDE.md` of
//! the project's above it. Its path is resolved (canonical), and every git
//! runs with `-c core.longpaths=true`. The checkout's path is a pure function
//! of the session and the agent, so a resume
//! finds an earlier run's own checkout and re-enters it; anything else at
//! that path is a refusal. The ctxloom-written config the run puts in the
//! checkout is hidden from the project's git status by the repository's
//! shared exclude file.
//!
//! The same adapter finds the scratch checkouts a crashed run left behind and
//! removes them WIP-safely: nested worktrees first, each checked, never
//! forced, and a doubtful state is never read as clean.
//!
//! Every git invocation is bounded (thirty seconds, Go's: [`DEFAULT_TIMEOUT`]):
//! a git that hangs is killed and reaped, and stops a run with a
//! `GitTimedOut` diagnostic rather than reading as "not a repository". The
//! program and the limit are [`Git`], which tests narrow to a stub script and
//! a tiny limit.

mod error;
mod git;
mod workspace;

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{WorktreeError, EN_US};
pub use git::{Git, GitError, User, DEFAULT_TIMEOUT};
pub use workspace::{branch_of, checkout_name, sanitize, GitWorkspaces, EXCLUDE_HEADER};

#[cfg(test)]
#[path = "error.test.rs"]
mod error_tests;

#[cfg(test)]
#[path = "fixtures.test.rs"]
mod fixtures;
