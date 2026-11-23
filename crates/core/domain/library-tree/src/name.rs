//! A library tree's resource name and its stream root (L1, L2, L10;
//! QUESTIONS B(a)).
//!
//! A project checkout's tree is `projects/{p}/checkouts/{c}/library`, `{c}`
//! the checkout's harp, minted once into its `.ctxloom/state/checkout` (out
//! of git, so a git worktree is a checkout of its own). Its root is the
//! UUIDv5 of that name ([`sessions::root_of`]), as every ctxloom root is.
//!
//! The home's tree is named `users/me/library` whatever home it is, and is
//! rooted on the home's OWN checkout harp (`<home>/.ctxloom/state/checkout`):
//! the UUIDv5 of `users/me/checkouts/{c}/library`. So each `CTXLOOM_DAEMON_ROOT` a
//! command names is a stream of its own, while the API's name stays one.

use std::fmt;

use sessions::{ProjectName, SessionRejection};

use crate::TreeKind;

/// Where a tree keeps its checkout's harp, relative to its `.ctxloom/`.
pub const CHECKOUT_FILE: &str = "state/checkout";

const PROJECTS: &str = "projects";
const CHECKOUTS: &str = "checkouts";
const LIBRARY: &str = "library";
/// The home's tree's name, whatever home it is.
pub const HOME_TREE: &str = "users/me/library";

/// A library tree's name.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub enum TreeName {
    /// `projects/{p}/checkouts/{c}/library`.
    Checkout {
        project: ProjectName,
        checkout: String,
    },
    /// `users/me/library`, rooted on the home's checkout harp `checkout`.
    Home { checkout: String },
}

impl TreeName {
    /// The tree of the checkout `checkout` (a harp) of `project`.
    pub fn checkout(project: ProjectName, checkout: &str) -> Result<TreeName, SessionRejection> {
        sessions::validate(checkout)?;
        Ok(TreeName::Checkout {
            project,
            checkout: checkout.to_string(),
        })
    }

    /// The home's tree, its checkout harp `checkout`.
    pub fn home(checkout: &str) -> Result<TreeName, SessionRejection> {
        sessions::validate(checkout)?;
        Ok(TreeName::Home {
            checkout: checkout.to_string(),
        })
    }

    /// `name` read as `projects/{p}/checkouts/{c}/library`; none for any
    /// other shape. The home's name says no harp, so it is never parsed
    /// back to a tree ([`TreeName::home`] builds it).
    pub fn parse_checkout(name: &str) -> Option<TreeName> {
        match name.split('/').collect::<Vec<_>>()[..] {
            [PROJECTS, project, CHECKOUTS, checkout, LIBRARY] => {
                let project = ProjectName::new(project).ok()?;
                TreeName::checkout(project, checkout).ok()
            }
            _ => None,
        }
    }

    /// Which kind of tree it is.
    pub fn kind(&self) -> TreeKind {
        match self {
            TreeName::Checkout { .. } => TreeKind::Checkout,
            TreeName::Home { .. } => TreeKind::Home,
        }
    }

    /// The checkout's harp.
    pub fn checkout_harp(&self) -> &str {
        match self {
            TreeName::Checkout { checkout, .. } | TreeName::Home { checkout } => checkout,
        }
    }

    /// The name its root is the UUIDv5 of: the name itself for a checkout,
    /// `users/me/checkouts/{c}/library` for the home.
    fn rooted_on(&self) -> String {
        match self {
            TreeName::Checkout { .. } => self.to_string(),
            TreeName::Home { checkout } => format!("users/me/{CHECKOUTS}/{checkout}/{LIBRARY}"),
        }
    }

    /// Its stream's root.
    pub fn root(&self) -> angzarr_embedded::v1::Uuid {
        tree_root(&self.rooted_on())
    }
}

impl fmt::Display for TreeName {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            TreeName::Checkout { project, checkout } => {
                write!(f, "{project}/{CHECKOUTS}/{checkout}/{LIBRARY}")
            }
            TreeName::Home { .. } => f.write_str(HOME_TREE),
        }
    }
}

/// The kind of the tree named `name`: the home's for `users/me/library`,
/// a checkout's otherwise.
pub fn kind_of(name: &str) -> TreeKind {
    if name == HOME_TREE {
        TreeKind::Home
    } else {
        TreeKind::Checkout
    }
}

/// The stream root of the tree whose rooting name is `name`: its UUIDv5.
pub fn tree_root(name: &str) -> angzarr_embedded::v1::Uuid {
    angzarr_embedded::v1::Uuid::from_bytes(*sessions::root_of(name).as_bytes())
}

#[cfg(test)]
#[path = "name.test.rs"]
mod tests;
