---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - testing
  - tdd
content_hash: sha256:f2500108d790c274e1e8bff3e9acce41da280c5e0bc624716daa7d908b323052
---
# TDD for LLM-Written Code

LLMs are non-deterministic generators. Tests are the deterministic gate around them.

## The Rule

Demand test-driven development from the LLM. Tests first, implementation second. Non-negotiable.

## Workflow

1. Describe the requirement
2. LLM writes tests against the requirement (not implementation)
3. Review tests: do they capture the requirement, edge cases, failure modes?
4. LLM implements to make tests pass
5. Run tests

## Why

The test suite is the contract. Generated code is checked against it. Without tests-first, the model fills the contract with whatever it produced and there is no separate ground truth.

## Anti-Patterns

- LLM writes implementation, then writes tests against the implementation. Tests confirm the bug, not the requirement.
- Vacuous assertions. `assert!(true)` adjacent to a function call passes without verifying anything.
- Implementation-coupled tests. `assert_eq!(hash("x"), 0x7a3f...)` is brittle to refactoring and blind to behavior.
- Skipping step 3. Review of tests is the load-bearing review; review of implementation is downstream.

## What Tests Document

Document the **problem**, not the solution. A good test names the requirement in the test name, sets up inputs, runs the function, and asserts the observable behavior.

## Verification

```
Show me the tests before the implementation. I will review the tests, approve them, then you write the implementation.
```
