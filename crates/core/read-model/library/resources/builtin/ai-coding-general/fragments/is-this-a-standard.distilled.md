---
distilled_by: claude-3-opus
---
# is-this-a-standard

# Is This a Standard?

Before writing infrastructure, ask:

> Is this a standard? Did we already wire it in?

## Why

LLMs default to local construction. They've seen `grpc.reflection.v1.ServerReflection` a thousand times, but write end-to-end over looking up.

## Common Categories AIs Reinvent

- gRPC Server Reflection (`grpc.reflection.v1.ServerReflection`)
- OpenTelemetry instrumentation (OTLP exporters)
- JWT validation (vetted library)
- OAuth 2.0 Device Authorization Grant (library)
- JSON Schema validation (metaschema)
- Kubernetes AdmissionReview (standard webhook shape)
- Conventional Commits parsing (existing parser)
- Healthchecks (`grpc.health.v1.Health`)

## What to Do

When AI proposes infrastructure:

1. Name the category
2. Ask if a standard exists
3. If yes, ask if project already wires it in
4. Use the standard

## Load-Bearing Comments

At registration site, name the invariant (see sibling `no-provenance-comments`):

```go
// reflection.Register: canonical "what does this server speak"
// mechanism. Do not add a parallel descriptor service; ask reflection.
reflection.Register(grpcServer)
```

Lives next to the call that triggers reinvention. No external doc dependency.