# ctxloom fork of hashicorp/go-plugin

Vendored copy of `github.com/hashicorp/go-plugin@v1.7.0`, wired in via a relative
`replace` directive in the repo-root `go.mod`.

## Changes vs upstream (two hunks, both in `server.go`)

### Hunk 1 — `serverListener()`: env gate onto the TCP listener path

```go
if runtime.GOOS == "windows" || os.Getenv("PLUGIN_LISTEN_TCP") == "1" {
	return serverListener_tcp()
}
```

Upstream selects the unix-vs-TCP listener purely by the plugin server's own
compile-time `runtime.GOOS`, so a plugin server running **inside a Linux
container** always picks a unix socket. On macOS/Windows the container runs in a
Linux VM (Docker Desktop): a unix socket created in a bind-mounted dir is not a
live endpoint on the host kernel, so the host client cannot dial it. Setting
`PLUGIN_LISTEN_TCP=1` forces the plugin onto go-plugin's **existing** TCP
listener path (honoring `PLUGIN_MIN_PORT`/`PLUGIN_MAX_PORT`), which ctxloom
publishes to host loopback. ctxloom injects `PLUGIN_LISTEN_TCP=1` only when the
HOST is non-Linux (or when the same var is set in the environment, the
integration-test hook); the Linux default keeps the fast, verified unix path.

### Hunk 2 — `serverListener_tcp()`: bind all interfaces under the env gate

```go
bindHost := "127.0.0.1"
if os.Getenv("PLUGIN_LISTEN_TCP") == "1" {
	bindHost = "0.0.0.0"
}
for port := minPort; port <= maxPort; port++ {
	address := fmt.Sprintf("%s:%d", bindHost, port)
	...
```

When forced onto TCP for the container transport (`PLUGIN_LISTEN_TCP=1`), the
listener binds **all interfaces** (`0.0.0.0`) instead of container-loopback. A
published port (docker/podman `-p 127.0.0.1:P:P`) DNATs host loopback to the
container's *routable* interface, never its loopback, so a `127.0.0.1`-bound
listener inside the container is unreachable from the host (the gRPC dial gets
an EOF reading the server preface). Binding `0.0.0.0` makes the published port
reach the listener; the host still exposes it on loopback only. Native-Windows
TCP (no env flag) keeps upstream's `127.0.0.1` — it is a same-host transport
with no port publish. The host client maps the announced `0.0.0.0:P` back to
`127.0.0.1:P` via its `AddrTranslator` before dialing.

## Provenance / trimming

Copied from the module cache, minus `examples/`, `test/`, `.github/`, `docs/`
(not needed to build the packages ctxloom imports: the root package and
`./runner`). No other source was modified.

## TODO before release

Move this to a real org fork (`github.com/ctxloom/go-plugin`) and pin it, rather
than shipping a vendored tree under a relative replace. Tracked as a deferred
item on the macOS container-isolation workstream.
