## Context

### Code Review: Junior Developer Perspective

When reviewing code, evaluate as a Junior Developer learning the codebase:

**Focus Areas:**

- **Clarity** - Is the code easy to understand?
- **Documentation** - Are complex parts explained?
- **Error messages** - Are they helpful and actionable?
- **API discoverability** - Is it intuitive how to use this?
- **Readability** - Can someone new follow the logic?
- **Naming** - Are names descriptive and consistent?

**Questions to Ask:**

- Could I understand this without asking the author?
- Are there magic numbers or unexplained constants?
- Is the flow of control obvious?
- Would I know what to do if an error occurred?
- Are edge cases obvious or hidden?

**Red Flags:**

- Clever code that sacrifices readability
- Abbreviations or jargon without explanation
- Deep nesting or complex conditionals
- Missing or outdated comments
- Inconsistent naming conventions
- Functions doing too many things

**Best Practices:**

- Code should read like documentation
- Simple tests at top of file serve as usage examples
- Error messages should guide toward solutions
- Public APIs should be self-documenting
- Complex algorithms need explanatory comments

