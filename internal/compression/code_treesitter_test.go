//go:build treesitter

package compression

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeCompressor_ConcurrentCompress exercises a single shared compressor
// from many goroutines parsing the same language concurrently. With the old
// single cached parser this raced (TSParser is stateful C); the per-language
// sync.Pool makes it safe. Run under -race to catch a regression.
func TestCodeCompressor_ConcurrentCompress(t *testing.T) {
	c := NewCodeCompressor()
	input := "package main\n\nfunc Foo(a int) int {\n\treturn a + 1\n}\n"

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := c.Compress(context.Background(), ContentTypeGo, input, 0.5)
			assert.NoError(t, err)
			assert.Contains(t, result.Content, "func Foo")
		}()
	}
	wg.Wait()
}

func TestCodeCompressor_Go(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	input := `package main

import (
	"fmt"
	"strings"
)

// User represents a user in the system.
type User struct {
	ID   int
	Name string
	Email string
}

// NewUser creates a new user with the given name.
// It validates the name and returns an error if invalid.
func NewUser(name string) (*User, error) {
	if name == "" {
		return nil, fmt.Errorf("name cannot be empty")
	}
	// Generate ID
	id := generateID()
	// Create user
	return &User{
		ID:   id,
		Name: strings.TrimSpace(name),
	}, nil
}

// GetFullName returns the user's full display name.
func (u *User) GetFullName() string {
	return fmt.Sprintf("%s (%d)", u.Name, u.ID)
}

func generateID() int {
	// Complex ID generation logic
	// with multiple steps
	// and calculations
	return 42
}
`

	result, err := c.Compress(ctx, ContentTypeGo, input, 0.5)
	require.NoError(t, err)

	// Should preserve package
	assert.Contains(t, result.Content, "package main")

	// Should preserve imports
	assert.Contains(t, result.Content, "import")
	assert.Contains(t, result.Content, `"fmt"`)

	// Should preserve type definition
	assert.Contains(t, result.Content, "type User struct")

	// Should preserve function signatures
	assert.Contains(t, result.Content, "func NewUser(name string)")
	assert.Contains(t, result.Content, "func (u *User) GetFullName()")

	// Should elide function bodies
	assert.Contains(t, result.Content, "{ ... }")

	// Should NOT contain implementation details (function bodies)
	assert.NotContains(t, result.Content, "Complex ID generation")
	assert.NotContains(t, result.Content, "Generate ID")
	assert.NotContains(t, result.Content, "Create user")

	// Should achieve compression
	assert.Less(t, result.Ratio, 0.7, "Expected significant compression")

	t.Logf("Compression ratio: %.2f%%", result.Ratio*100)
	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

// TestCodeCompressor_Go_EmptyExtractionFallsBackToVerbatim (U046-F02) proves
// content that parses under the Go grammar but yields ZERO recognized
// top-level node kinds (extractGo's goVerbatim map + function/method
// declarations + comments) degrades to a verbatim pass-through, not an
// empty Content with a nil error and Ratio: 0.0 — previously indistinguishable
// from a successful, extreme compression. Prose wrongly routed to the Go
// compressor (U046-F01's own failure mode, before that sniff was deleted) is
// exactly the shape that used to trigger this: the grammar emits only ERROR/
// expression_statement nodes, none of which extractGo's switch handles.
func TestCodeCompressor_Go_EmptyExtractionFallsBackToVerbatim(t *testing.T) {
	c := NewCodeCompressor()
	input := "packages are great\n\nAlways pin your dependencies for reproducible builds.\n"

	result, err := c.Compress(context.Background(), ContentTypeGo, input, 0.5)
	require.NoError(t, err)
	assert.Equal(t, input, result.Content, "zero recognized nodes must fall back to the original content verbatim")
	assert.Equal(t, 1.0, result.Ratio, "a verbatim fallback reports Ratio 1.0, never 0.0 (which read as a successful compression)")
}

func TestCodeCompressor_Python(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	input := `"""
Module for user management.
"""
import os
from typing import Optional, List
from dataclasses import dataclass

@dataclass
class User:
    """Represents a user."""
    id: int
    name: str
    email: Optional[str] = None

class UserService:
    """Service for managing users."""

    def __init__(self, db_connection):
        """Initialize with database connection."""
        self.db = db_connection
        self._cache = {}

    def get_user(self, user_id: int) -> Optional[User]:
        """Fetch a user by ID."""
        if user_id in self._cache:
            return self._cache[user_id]

        # Query database
        result = self.db.query(
            "SELECT * FROM users WHERE id = ?",
            user_id
        )
        if result:
            user = User(**result)
            self._cache[user_id] = user
            return user
        return None

    @staticmethod
    def validate_email(email: str) -> bool:
        """Check if email is valid."""
        return "@" in email and "." in email

def main():
    """Entry point."""
    service = UserService(get_db())
    user = service.get_user(1)
    print(user)
`

	result, err := c.Compress(ctx, ContentTypePython, input, 0.5)
	require.NoError(t, err)

	// Should preserve imports
	assert.Contains(t, result.Content, "import os")
	assert.Contains(t, result.Content, "from typing import")

	// Should preserve class definition
	assert.Contains(t, result.Content, "class User")
	assert.Contains(t, result.Content, "class UserService")

	// Should preserve method signatures
	assert.Contains(t, result.Content, "def get_user")
	assert.Contains(t, result.Content, "def validate_email")

	// Should elide bodies
	assert.Contains(t, result.Content, "...")

	t.Logf("Compression ratio: %.2f%%", result.Ratio*100)
	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestCodeCompressor_JavaScript(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	input := `import express from 'express';
import { UserService } from './services/user';
import { validateRequest } from './middleware';

const DEFAULT_PORT = 3000;

export class UserController {
  constructor(userService) {
    this.userService = userService;
    this.router = express.Router();
    this.initRoutes();
  }

  initRoutes() {
    this.router.get('/users', this.getUsers.bind(this));
    this.router.get('/users/:id', this.getUserById.bind(this));
    this.router.post('/users', validateRequest, this.createUser.bind(this));
  }

  async getUsers(req, res) {
    try {
      const users = await this.userService.findAll();
      res.json(users);
    } catch (error) {
      console.error('Failed to get users:', error);
      res.status(500).json({ error: 'Internal server error' });
    }
  }

  async getUserById(req, res) {
    const { id } = req.params;
    const user = await this.userService.findById(id);
    if (!user) {
      return res.status(404).json({ error: 'User not found' });
    }
    res.json(user);
  }

  async createUser(req, res) {
    const userData = req.body;
    const user = await this.userService.create(userData);
    res.status(201).json(user);
  }
}

export function createApp() {
  const app = express();
  app.use(express.json());
  return app;
}
`

	result, err := c.Compress(ctx, ContentTypeJavaScript, input, 0.5)
	require.NoError(t, err)

	// Should preserve imports
	assert.Contains(t, result.Content, "import express")
	assert.Contains(t, result.Content, "import { UserService }")

	// Should preserve class
	assert.Contains(t, result.Content, "class UserController")

	// Should preserve function exports
	assert.Contains(t, result.Content, "function createApp")

	t.Logf("Compression ratio: %.2f%%", result.Ratio*100)
	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

// TestCodeCompressor_JavaScriptExportedLongConst pins that an exported long
// non-arrow const declaration neither leaves a dangling "export " glued onto
// the following declaration nor silently drops the binding. (regression for the
// extractJSLexical >100-char drop)
func TestCodeCompressor_JavaScriptExportedLongConst(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	input := `export const config = { host: 'localhost', port: 3000, timeout: 30000, retries: 5, verbose: true, logLevel: 'info' };

export function createApp() {
  return {};
}
`

	result, err := c.Compress(ctx, ContentTypeJavaScript, input, 0.5)
	require.NoError(t, err)

	// The binding name must survive in some form.
	assert.Contains(t, result.Content, "config")
	// The exported function after the long const must still be recognizable.
	assert.Contains(t, result.Content, "function createApp")
	// The buggy output dropped the const body and glued the next export onto the
	// dangling "export " prefix, producing "export export function ...".
	assert.NotContains(t, result.Content, "export export")
	// No line should be a bare dangling "export".
	for _, line := range strings.Split(result.Content, "\n") {
		assert.NotEqual(t, "export", strings.TrimSpace(line), "dangling export line in: %q", result.Content)
	}

	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

// TestCodeCompressor_PythonModuleAssignmentsAndAsync pins that module-level
// assignments are preserved (like Go/Rust/JS top-level const/var) and that the
// async keyword survives in compressed signatures. (regression for code_python)
func TestCodeCompressor_PythonModuleAssignmentsAndAsync(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	input := `import os

API_URL = "https://example.com"
__all__ = ["fetch"]

async def fetch(url):
    return await do(url)

def sync_only():
    return 1
`

	result, err := c.Compress(ctx, ContentTypePython, input, 0.5)
	require.NoError(t, err)

	// Module-level assignments preserved.
	assert.Contains(t, result.Content, "API_URL")
	assert.Contains(t, result.Content, "__all__")
	// async keyword preserved in the signature.
	assert.Contains(t, result.Content, "async def fetch")
	// Plain def still works.
	assert.Contains(t, result.Content, "def sync_only")

	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestCodeCompressor_Rust(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	input := `use std::collections::HashMap;
use std::sync::Arc;

mod config;
pub mod handlers;

/// A user in the system.
#[derive(Debug, Clone)]
pub struct User {
    pub id: u64,
    pub name: String,
    pub email: Option<String>,
}

impl User {
    /// Creates a new user with the given name.
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            id: generate_id(),
            name: name.into(),
            email: None,
        }
    }

    /// Sets the user's email.
    pub fn with_email(mut self, email: impl Into<String>) -> Self {
        self.email = Some(email.into());
        self
    }

    /// Validates the user data.
    fn validate(&self) -> Result<(), ValidationError> {
        if self.name.is_empty() {
            return Err(ValidationError::EmptyName);
        }
        Ok(())
    }
}

pub trait UserRepository {
    fn find_by_id(&self, id: u64) -> Option<User>;
    fn save(&mut self, user: User) -> Result<(), Error>;
}

fn generate_id() -> u64 {
    // Complex ID generation
    42
}
`

	result, err := c.Compress(ctx, ContentTypeRust, input, 0.5)
	require.NoError(t, err)

	// Should preserve use statements
	assert.Contains(t, result.Content, "use std::collections::HashMap")

	// Should preserve struct
	assert.Contains(t, result.Content, "pub struct User")

	// Should preserve impl block with method signatures
	assert.Contains(t, result.Content, "impl User")
	assert.Contains(t, result.Content, "pub fn new")
	assert.Contains(t, result.Content, "pub fn with_email")

	// Should preserve trait
	assert.Contains(t, result.Content, "pub trait UserRepository")

	t.Logf("Compression ratio: %.2f%%", result.Ratio*100)
	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestCodeCompressor_Java(t *testing.T) {
	t.Skip("Java tree-sitter integration needs debugging - class body extraction incomplete")

	c := NewCodeCompressor()
	ctx := context.Background()

	// Simplified Java without annotations for cleaner AST
	input := `package com.example.users;

import java.util.List;
import java.util.Optional;

public class User {
    private Long id;
    private String name;

    public User() {
    }

    public Long getId() {
        return id;
    }

    public void setName(String name) {
        if (name == null) {
            throw new IllegalArgumentException("Name cannot be empty");
        }
        this.name = name;
    }
}
`

	result, err := c.Compress(ctx, ContentTypeJava, input, 0.5)
	require.NoError(t, err)

	// Should preserve package
	assert.Contains(t, result.Content, "package com.example.users")

	// Should preserve imports
	assert.Contains(t, result.Content, "import java.util.List")

	// Should preserve class
	assert.Contains(t, result.Content, "public class User")

	// Should preserve method signatures
	assert.Contains(t, result.Content, "public Long getId()")
	assert.Contains(t, result.Content, "public void setName")

	// Should elide bodies
	assert.Contains(t, result.Content, "{ ... }")

	t.Logf("Compression ratio: %.2f%%", result.Ratio*100)
	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestCodeCompressor_PreservesSignatures(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	tests := []struct {
		name     string
		input    string
		expected []string // Strings that must be in output
	}{
		{
			name: "Go generics",
			input: `package main

func Map[T, U any](items []T, fn func(T) U) []U {
	result := make([]U, len(items))
	for i, item := range items {
		result[i] = fn(item)
	}
	return result
}`,
			expected: []string{"func Map[T, U any]", "[]T", "func(T) U", "[]U"},
		},
		{
			name: "Go interface",
			input: `package main

type Reader interface {
	Read(p []byte) (n int, err error)
}`,
			expected: []string{"type Reader interface", "Read(p []byte)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := c.Compress(ctx, ContentTypeGo, tt.input, 0.5)
			require.NoError(t, err)

			for _, exp := range tt.expected {
				assert.True(t, strings.Contains(result.Content, exp),
					"Expected output to contain %q\nGot:\n%s", exp, result.Content)
			}
		})
	}
}
