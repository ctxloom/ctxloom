//! The daemon's prost types, generated into `OUT_DIR` with angzarr's
//! settings. Generated code is never committed. The protos are the daemon's
//! drafts (ruling 132); their resource messages are what the read models
//! project for `HandleSync` (S32), so they are compiled from slice 1 on.
//!
//! Two passes more, over the descriptor set the first wrote (slice 2 step
//! 5):
//!
//! - each refusal message's declared status, read from its
//!   `(io.angzarr.v1.refusal_status)` option (the extension's number is its
//!   `google.rpc.Code` number), as the generated `Refusal::REFUSAL_STATUS`
//!   of `refusals.rs`: the one way Rust names it, as a domain's generated
//!   layer does;
//! - the `Ctxloom` service's tonic stubs (`server` and `client` features,
//!   tonic's codegen only, its prost codec), the messages extern-pathed onto
//!   the first pass's, so nothing is generated twice; default stubs, so an
//!   RPC the server does not implement answers UNIMPLEMENTED.

// A build script runs at build time, never in the core: it reads the
// descriptor set the first pass wrote and writes the passes' output, under
// OUT_DIR, as angzarr-build itself does.
#![allow(clippy::disallowed_methods)]

use std::path::{Path, PathBuf};

/// The package the passes read.
const PACKAGE: &str = "ctxloom.daemon.v1";

/// angzarr's `refusal_status` extension of `MessageOptions`.
const REFUSAL_STATUS_FIELD: u64 = 50104;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    angzarr_build::wire(
        &[
            "ctxloom/daemon/v1/daemon.proto",
            "ctxloom/daemon/v1/discovery.proto",
            "ctxloom/daemon/v1/launch.proto",
            "ctxloom/daemon/v1/needs.proto",
        ],
        &["../../../../proto"],
    )?;
    let out = PathBuf::from(std::env::var("OUT_DIR")?);
    let set = std::fs::read(out.join("angzarr.descriptors"))?;
    std::fs::write(out.join("refusals.rs"), refusals(&set)?)?;
    let server = std::env::var_os("CARGO_FEATURE_SERVER").is_some();
    let client = std::env::var_os("CARGO_FEATURE_CLIENT").is_some();
    if server || client {
        services(&out, &set, server, client)?;
    }
    Ok(())
}

/// The service stubs, into `<out>/services/ctxloom.daemon.v1.rs`.
fn services(
    out: &Path,
    set: &[u8],
    server: bool,
    client: bool,
) -> Result<(), Box<dyn std::error::Error>> {
    use prost::Message;
    let dir = out.join("services");
    std::fs::create_dir_all(&dir)?;
    let fds = prost_types::FileDescriptorSet::decode(set)?;
    tonic_prost_build::configure()
        .build_server(server)
        .build_client(client)
        .build_transport(false)
        .generate_default_stubs(true)
        .extern_path(format!(".{PACKAGE}"), "crate::v1")
        .extern_path(".io.angzarr.v1", "crate::angzarr_v1")
        .out_dir(&dir)
        .compile_fds(fds)?;
    Ok(())
}

/// A reader of protobuf's wire format: just what the walk needs.
struct Wire<'a>(&'a [u8]);

impl<'a> Wire<'a> {
    fn varint(&mut self) -> Result<u64, String> {
        let mut value = 0u64;
        for shift in (0..64).step_by(7) {
            let (&byte, rest) = self.0.split_first().ok_or("a varint runs past the end")?;
            self.0 = rest;
            value |= u64::from(byte & 0x7f) << shift;
            if byte & 0x80 == 0 {
                return Ok(value);
            }
        }
        Err("a varint longer than ten bytes".to_string())
    }

    /// The next field: its number, and its bytes when it is length-delimited
    /// or its value when it is a varint; other wire types are skipped.
    fn field(&mut self) -> Result<Option<(u64, Field<'a>)>, String> {
        if self.0.is_empty() {
            return Ok(None);
        }
        let key = self.varint()?;
        let field = match key & 7 {
            0 => Field::Varint(self.varint()?),
            1 => {
                self.take(8)?;
                Field::Skipped
            }
            2 => {
                let len = usize::try_from(self.varint()?).map_err(|e| e.to_string())?;
                Field::Bytes(self.take(len)?)
            }
            5 => {
                self.take(4)?;
                Field::Skipped
            }
            other => return Err(format!("wire type {other} is not read here")),
        };
        Ok(Some((key >> 3, field)))
    }

    fn take(&mut self, n: usize) -> Result<&'a [u8], String> {
        if self.0.len() < n {
            return Err("a field runs past the end".to_string());
        }
        let (head, rest) = self.0.split_at(n);
        self.0 = rest;
        Ok(head)
    }
}

enum Field<'a> {
    Varint(u64),
    Bytes(&'a [u8]),
    Skipped,
}

/// Every top-level message of `PACKAGE` that declares a refusal status,
/// with it: `refusals.rs`.
fn refusals(set: &[u8]) -> Result<String, String> {
    let mut found = Vec::new();
    let mut files = Wire(set);
    while let Some((number, field)) = files.field()? {
        let (1, Field::Bytes(file)) = (number, field) else {
            continue;
        };
        let mut package = String::new();
        let mut messages = Vec::new();
        let mut fields = Wire(file);
        while let Some((number, field)) = fields.field()? {
            match (number, field) {
                (2, Field::Bytes(text)) => package = String::from_utf8_lossy(text).into_owned(),
                (4, Field::Bytes(message)) => messages.push(message),
                _ => {}
            }
        }
        if package != PACKAGE {
            continue;
        }
        for message in messages {
            if let Some(declared) = declared(message)? {
                found.push(declared);
            }
        }
    }
    found.sort();
    let mut source = String::from(
        "/// A typed refusal message of the daemon's API: the status it is refused\n\
         /// with, the `google.rpc.Code` number its `(refusal_status)` declares.\n\
         pub trait Refusal: ::prost::Name {\n    const REFUSAL_STATUS: i32;\n}\n",
    );
    for (name, status) in found {
        source.push_str(&format!(
            "\nimpl Refusal for crate::v1::{name} {{\n    const REFUSAL_STATUS: i32 = {status};\n}}\n"
        ));
    }
    Ok(source)
}

/// A message's name and declared refusal status, if it declares one.
fn declared(message: &[u8]) -> Result<Option<(String, u64)>, String> {
    let mut name = String::new();
    let mut status = None;
    let mut fields = Wire(message);
    while let Some((number, field)) = fields.field()? {
        match (number, field) {
            (1, Field::Bytes(text)) => name = String::from_utf8_lossy(text).into_owned(),
            (7, Field::Bytes(options)) => {
                let mut options = Wire(options);
                while let Some((number, field)) = options.field()? {
                    if let (REFUSAL_STATUS_FIELD, Field::Varint(value)) = (number, field) {
                        status = Some(value);
                    }
                }
            }
            _ => {}
        }
    }
    Ok(status.map(|status| (name, status)))
}
