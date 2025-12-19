## Context

### Git Workflow

**Commands:**

- Use `git log` to view commit history and understand context
- Use `git rm` to remove files (not `rm`) - ensures proper tracking
- Use `git status` to check state before operations

**Cleanup:**

- Kill background processes when no longer needed
- Remove unused code, files, imports, variables
- No dead code in the repository

**File Removal:**

```bash
# Correct - tracked by git
git rm path/to/file

# Incorrect - leaves git confused
rm path/to/file
```

