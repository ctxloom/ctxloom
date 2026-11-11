---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - standards
  - review
  - architecture
notes: |-
  Defends against reinvented infrastructure: a model can produce a
  plausible descriptor service, JWT check, or health endpoint
  end-to-end, so by default it writes one instead of looking up the
  standard — and the hand-rolled copy shadows a mechanism the project
  may already wire in. The guidance is shaped as a question protocol
  rather than a rule list because the trigger is a category ("this
  sounds like server discovery"), not a named API; the
  registration-site comment puts the answer next to the call where
  the temptation recurs. Origin: the recurring reinvention pattern
  itself, observed across model output generally — no single
  incident behind it.
content_hash: sha256:786c9b04f480632a165d312876adcfdd94fd3d2c673e048c15eb7740455845dd
---
# Is This a Standard?

Before writing infrastructure, ask one question:

> Is this a standard?

If the answer is yes, ask:

> Did we already wire it in?

## Why

LLMs default to local construction. They have seen a thousand examples of "service that lists what a server does" and can produce another one. They have also seen `grpc.reflection.v1.ServerReflection` a thousand times, but the prompt was about your problem, not the standard. The model writes what it can write end-to-end over what it would have to look up.

## Common Categories AIs Reinvent

- gRPC Server Reflection (use `grpc.reflection.v1.ServerReflection`)
- OpenTelemetry instrumentation (use OTLP exporters, not custom telemetry)
- JWT validation (use a vetted library, not hand-rolled)
- OAuth 2.0 Device Authorization Grant (use a library)
- JSON Schema validation (use the metaschema)
- Kubernetes AdmissionReview (use the standard webhook shape)
- Conventional Commits parsing (use an existing parser)
- Healthchecks (use `grpc.health.v1.Health`)

## What to Do

When the AI proposes writing infrastructure:

1. Name the category ("this sounds like server discovery")
2. Ask whether a standard exists for that category
3. If yes, ask whether the project already wires it in
4. Use the standard

## Load-Bearing Comments

At the registration site of a standard mechanism, leave a comment that names the invariant:

```go
// reflection.Register: the canonical "what does this server speak"
// mechanism. Do not add a parallel descriptor service; ask reflection.
reflection.Register(grpcServer)
```

These comments live next to the call that triggers the temptation to reinvent. They do not depend on the AI consulting an external document.
