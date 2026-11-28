//! The user-settings domain's wire types: prost messages generated from
//! `proto/ctxloom/settings/v1`. The wire form only; domain code works on
//! domain types converted from these at the boundary.

/// The generated package, nested as its proto package is.
pub mod ctxloom {
    pub mod settings {
        pub mod v1 {
            include!(concat!(env!("OUT_DIR"), "/ctxloom.settings.v1.rs"));
        }
    }
}

/// `ctxloom.settings.v1`.
pub use ctxloom::settings::v1;

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
