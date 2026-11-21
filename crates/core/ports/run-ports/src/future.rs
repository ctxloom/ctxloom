//! A boxed future, `Send`: what a use case holding a `dyn` port awaits (the
//! frontend's delivery, pause and wait). The per-run hook listener's port
//! that once lived here (`HookServer`, `Serving`) is gone with the listener
//! (slice 4 U7, owner ruling s4-Q3).

use std::future::Future;
use std::pin::Pin;

/// A boxed future, `Send`: what a use case holding a `dyn` port awaits.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;
