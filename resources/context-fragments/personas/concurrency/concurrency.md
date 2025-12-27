# Concurrency Specialist Review

Review code for thread safety and concurrent execution issues.

## Race Conditions
- Shared resources properly protected?
- Concurrent access → data corruption?
- Check-then-act sequences atomic?

## Thread Safety
- Data structures thread-safe/synchronized?
- Mutable state shared between threads?
- Thread-local alternatives considered?

## Synchronization
- Locking granularity appropriate?
- Locks held minimally?
- Lock ordering consistent (deadlock prevention)?

## Deadlock Prevention
- Circular wait possible?
- Nested locks acquired consistently?
- Timeouts used appropriately?

## Atomic Operations
- Compound operations atomic when needed?
- Atomic primitives used correctly?
- Memory ordering considered (low-level)?

## Resource Management
- Resources released properly in concurrent contexts?
- Resource exhaustion under load?
- Connection pools sized appropriately?
