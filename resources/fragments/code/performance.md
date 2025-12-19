## Context

### Performance Guidelines

**Optimization Process:**

1. Profile before optimizing - measure, don't guess
2. Identify actual bottlenecks
3. Optimize the critical path
4. Measure improvement

**Documentation:**

- Document performance requirements in tests
- State algorithmic complexity for non-trivial algorithms
- Note memory allocation patterns when relevant

**Best Practices:**

- Use appropriate data structures for the access pattern
- Consider memory allocation patterns (pooling, pre-allocation)
- Avoid premature optimization
- Cache expensive computations when appropriate
- Be aware of N+1 query problems

**When to Optimize:**

- When profiling shows a bottleneck
- When performance requirements are not met
- When scaling issues are identified

**When NOT to Optimize:**

- Before measuring
- For code that runs rarely
- At the cost of readability (without justification)

