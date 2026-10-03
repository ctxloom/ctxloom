// Package portent maps an arbitrary input to a TCP/UDP port number inside a
// chosen range, deterministically: the same input and range always give the
// same port, on every platform and in every implementation that follows the
// algorithm below.
//
// It uses only the Go standard library. The algorithm is specified here
// exactly, so it can be reimplemented in another language. The golden vectors
// in the tests are the conformance check for any reimplementation.
//
// # Ranges
//
// A [Range] is an inclusive pair Lo..Hi of port numbers. A valid range has
// 1 <= Lo <= Hi <= 65535. Port 0 is excluded because it means "any port" to
// bind(2) and is not a port a service can be reached on. [Range.Validate]
// reports what is wrong with a range. [Pick] and [Candidates] panic with that
// same error when given an invalid range, because an invalid range is a
// programming error and not a runtime condition.
//
// The predefined ranges are conventions, not guarantees:
//
//   - [Privileged] (1-1023): on traditional Unix, binding needs root or
//     CAP_NET_BIND_SERVICE. This is OS-dependent. Linux can lower the bound
//     (net.ipv4.ip_unprivileged_port_start), recent macOS lets unprivileged
//     processes bind these ports on wildcard addresses, and Windows has no
//     privileged ports.
//   - [Registered] (1024-49151): the IANA User Ports range (RFC 6335).
//   - [Dynamic] (49152-65535): the IANA Dynamic/Private range (RFC 6335),
//     also the default ephemeral range on macOS and Windows.
//   - [Service] (1024-32767): the registered range stopped just below 32768,
//     where Linux's default ephemeral range (32768-60999) starts. It overlaps
//     no common OS default ephemeral range, so a port picked from it is not
//     one the OS hands out for outgoing connections. Administrators can change
//     the ephemeral ranges, so this is a default, not a promise.
//
// A picked port is never checked for availability. Something may already be
// listening on it. [Candidates] gives fallbacks in a deterministic order for
// that case.
//
// # Algorithm
//
// Let size = Hi - Lo + 1, so 1 <= size <= 65535.
//
// Draw sequence. Draw n, for n = 0, 1, 2, ..., is the first 8 bytes of
// SHA-256(input || BE32(n)), read as a big-endian unsigned 64-bit integer.
// BE32(n) is n as 4 big-endian bytes and || is concatenation. Each draw is
// used at most once, in order. One counter is shared by every step below, so
// a step starts at the draw after the last one the previous step used.
//
// Uniform(m), for 1 <= m < 2^64, returns a value in [0, m) without modulo
// bias, using rejection sampling. Let t = 2^64 mod m. Take the next draw v.
// If v < t, reject it and take the next draw. Otherwise return v mod m. The
// accepted draws [t, 2^64) are a whole number of multiples of m, so every
// residue is equally likely. Rejection happens with probability below
// m / 2^64, so in practice it never happens, but an implementation must still
// do it to match.
//
// Start. start = Uniform(size).
//
// Stride. If size == 1, stride = 1 and no draws are used. Otherwise repeat
// stride = 1 + Uniform(size - 1) until gcd(stride, size) == 1. The result is
// in [1, size) and coprime to size.
//
// Candidate i, for i = 0, 1, ..., size-1, is Lo + ((start + i*stride) mod size).
// Because stride is coprime to size, these are size distinct ports: every port
// in the range, each exactly once. [Candidates] yields them in that order and
// then stops.
//
// [Pick] is candidate 0, which is Lo + start. It needs only the draws for
// start.
//
// The order after the first candidate is an arithmetic walk. It is
// deterministic and it covers the range, but it is not a random permutation:
// two inputs that get the same stride walk in the same pattern.
package portent
