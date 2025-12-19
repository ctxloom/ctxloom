## Context

### Error Message Constants Pattern

Define error description text as constants shared between code and tests.

**Rationale:**

- Ensures consistency between thrown errors and test assertions
- Makes error messages easily discoverable and refactorable
- Reduces magic strings scattered across codebase
- Tests verify exact error messages without duplication

**Example (Python):**

```python
# errmsg/messages.py
ERROR_DIVIDE_BY_ZERO = "cannot divide by zero"

# calculator.py
from errmsg.messages import ERROR_DIVIDE_BY_ZERO

def divide(a, b):
    if b == 0:
        raise ValueError(ERROR_DIVIDE_BY_ZERO)
    return a / b

# test_calculator.py
from errmsg.messages import ERROR_DIVIDE_BY_ZERO

def test_divide_by_zero_raises_error():
    with pytest.raises(ValueError, match=ERROR_DIVIDE_BY_ZERO):
        divide(10, 0)
```

**Example (Go):**

```go
// errmsg/messages.go
const DivideByZero = "cannot divide by zero"

// Usage
return 0, errors.New(errmsg.DivideByZero)
```

