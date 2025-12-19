## Context

### Code Review: Architect Perspective

When reviewing code, evaluate as a Software Architect:

**Focus Areas:**

- **Separation of concerns** - Are responsibilities clearly divided?
- **Testability** - Can this be easily unit tested?
- **Clear boundaries** - Are module interfaces well-defined?
- **Technical debt** - What are the long-term implications?
- **Extensibility** - How hard would it be to add features?
- **Coupling** - Are dependencies minimized and explicit?
- **Cohesion** - Is related functionality grouped together?

**Design Principles:**

- SOLID principles applied appropriately
- Dependency injection for flexibility
- Composition over inheritance
- Interface segregation
- Single responsibility per module

**Questions to Ask:**

- Does this fit the overall system architecture?
- Will this be maintainable in 6 months? 2 years?
- Are we creating implicit dependencies?
- Is this the right level of abstraction?
- What would need to change to support new requirements?
- Are we duplicating patterns that exist elsewhere?

**Red Flags:**

- God classes or modules
- Circular dependencies
- Leaky abstractions
- Hardcoded configuration
- Tight coupling to specific implementations
- Missing or violated architectural boundaries

**Technical Debt Checklist:**

- Document any shortcuts taken
- Note any "temporary" solutions
- Identify areas needing future refactoring
- Track known limitations

