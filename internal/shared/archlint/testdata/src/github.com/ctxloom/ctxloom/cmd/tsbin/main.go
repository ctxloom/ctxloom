package main // want package:"reaches test-only tree via github.com/ctxloom/ctxloom/internal/tsuser" `shipped binary cmd/tsbin reaches the test-only tree through github.com/ctxloom/ctxloom/internal/tsuser`

import "github.com/ctxloom/ctxloom/internal/tsuser"

func main() { tsuser.Use() }
