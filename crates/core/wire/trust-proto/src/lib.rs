//! The trust domain's wire types: prost messages generated from
//! `proto/ctxloom/trust/v1` (step 8 U1). The wire form only; domain code
//! works on domain types converted from these at the boundary.

/// The generated package, nested as its proto package is.
pub mod ctxloom {
    pub mod trust {
        pub mod v1 {
            include!(concat!(env!("OUT_DIR"), "/ctxloom.trust.v1.rs"));
        }
    }
}

/// `ctxloom.trust.v1`.
pub use ctxloom::trust::v1;

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
