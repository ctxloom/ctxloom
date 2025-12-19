## Context

### Documentation and Planning Files

**ASK FIRST before creating:**

- Markdown files (*.md) - REFACTORING.md, architecture docs, etc.
- Planning or strategy documents
- Project tracking files
- Meta-documentation

**Exception:** README updates are permitted when adding new commands or features.

**Plan Files:**

- Place in `.plan/` directory
- Ensure `.plan/` is in `.gitignore` - plan files should not be committed
- Use descriptive names: `.plan/feature-name.md`

**DON'T:**

- Document progress/completed work in project files
- Include change history metadata in outputs
- Add "reorganized code to...", "refactored X to Y" comments

**Rationale:**

Version control handles history. Outputs represent current state, not accumulated changes.

