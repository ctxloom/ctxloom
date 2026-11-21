//! The library domain's wire types: prost messages generated from
//! `proto/ctxloom/library/v1` (step 10 U0). The wire form only; domain code
//! works on domain types converted from these at the boundary. The API's
//! own library messages are `daemon-proto`'s `ctxloom.v1`, not these.

/// The generated package, nested as its proto package is.
pub mod ctxloom {
    pub mod library {
        pub mod v1 {
            include!(concat!(env!("OUT_DIR"), "/ctxloom.library.v1.rs"));
        }
    }
}

/// `ctxloom.library.v1`.
pub use ctxloom::library::v1;

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
