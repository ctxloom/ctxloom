//! `grpc.health.v1.Health`'s Check, written by hand as the sink's other
//! messages are: its request and response (the response's enum is a varint
//! on the wire, read as its number), and a prost codec for tonic's generic
//! client. The status numbers are the standard's.

use std::marker::PhantomData;

use prost::Message;
use tonic::codec::{Codec, DecodeBuf, Decoder, EncodeBuf, Encoder};
use tonic::Status;

/// The Check's path.
pub(crate) const CHECK: &str = "/grpc.health.v1.Health/Check";

/// `ServingStatus.SERVING`.
pub(crate) const SERVING: i32 = 1;

/// `grpc.health.v1.HealthCheckRequest`: the empty service is the server's
/// overall health.
#[derive(Clone, PartialEq, Message)]
pub(crate) struct HealthCheckRequest {
    #[prost(string, tag = "1")]
    pub(crate) service: String,
}

/// `grpc.health.v1.HealthCheckResponse`.
#[derive(Clone, PartialEq, Message)]
pub(crate) struct HealthCheckResponse {
    #[prost(int32, tag = "1")]
    pub(crate) status: i32,
}

/// A codec encoding `E` and decoding `D` with prost.
pub(crate) struct Prost<E, D>(PhantomData<fn() -> (E, D)>);

impl<E, D> Default for Prost<E, D> {
    fn default() -> Self {
        Prost(PhantomData)
    }
}

impl<E, D> Codec for Prost<E, D>
where
    E: Message + Send + 'static,
    D: Message + Default + Send + 'static,
{
    type Encode = E;
    type Decode = D;
    type Encoder = Encode<E>;
    type Decoder = Decode<D>;

    fn encoder(&mut self) -> Encode<E> {
        Encode(PhantomData)
    }

    fn decoder(&mut self) -> Decode<D> {
        Decode(PhantomData)
    }
}

/// The encoder of [`Prost`].
pub(crate) struct Encode<E>(PhantomData<fn() -> E>);

impl<E: Message> Encoder for Encode<E> {
    type Item = E;
    type Error = Status;

    fn encode(&mut self, item: E, dst: &mut EncodeBuf<'_>) -> Result<(), Status> {
        item.encode(dst)
            .map_err(|e| Status::internal(format!("cannot encode: {e}")))
    }
}

/// The decoder of [`Prost`].
pub(crate) struct Decode<D>(PhantomData<fn() -> D>);

impl<D: Message + Default> Decoder for Decode<D> {
    type Item = D;
    type Error = Status;

    fn decode(&mut self, src: &mut DecodeBuf<'_>) -> Result<Option<D>, Status> {
        D::decode(src)
            .map(Some)
            .map_err(|e| Status::internal(format!("cannot decode: {e}")))
    }
}
