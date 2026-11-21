//! The approvals domain's wire types: prost messages generated from
//! `proto/ctxloom/approvals/v1` (step 8 U4). The wire form only; domain code
//! works on domain types converted from these at the boundary.

/// The generated package, nested as its proto package is.
pub mod ctxloom {
    pub mod approvals {
        pub mod v1 {
            include!(concat!(env!("OUT_DIR"), "/ctxloom.approvals.v1.rs"));
        }
    }
}

/// `ctxloom.approvals.v1`.
pub use ctxloom::approvals::v1;

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
