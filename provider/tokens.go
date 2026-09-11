package provider

import "context"

// TokenCounter is an optional provider capability for measuring a request's
// input tokens before generation. Implementations include message formatting,
// tools, and model-specific template settings, and may make a network request.
// A successful count is nonnegative; zero is valid. Failures must return an
// error rather than a guessed count. Implementations may be called concurrently.
// Counting does not submit a generation or update Usage accounting.
type TokenCounter interface {
	CountTokens(context.Context, Request) (int64, error)
}
