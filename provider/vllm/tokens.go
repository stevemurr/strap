package vllm

import (
	"context"
	"errors"
	"fmt"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

// CountTokens returns vLLM's /tokenize count for the supplied request using the
// same message translation, tools, and thinking settings as Submit. The server
// applies its tokenizer and chat template, including the generation prompt.
// This is an explicit network call, independent of generation and usage totals.
func (c *Client) CountTokens(ctx context.Context, input provider.Request) (int64, error) {
	base, err := chatwire.Encode(c.model, input)
	if err != nil {
		return 0, fmt.Errorf("vllm: count tokens: encode content: %w", err)
	}
	// Only prompt-affecting fields belong in the tokenization protocol.
	// Sampling settings and output budgets are generation-only parameters.
	wire := struct {
		chatwire.Chat
		AddGenerationPrompt bool            `json:"add_generation_prompt"`
		ChatTemplate        *templateFields `json:"chat_template_kwargs,omitempty"`
	}{base.Chat, true, c.generation.ChatTemplate}
	var result struct {
		Count *int64 `json:"count"`
	}
	if err := c.wire.Post(ctx, c.tokenizeEndpoint, wire, &result); err != nil {
		var responseError *chatwire.HTTPError
		if errors.As(err, &responseError) {
			err = &HTTPError{StatusCode: responseError.StatusCode, Body: responseError.Body}
		}
		return 0, fmt.Errorf("vllm: count tokens: %w", err)
	}
	if result.Count == nil || *result.Count < 0 {
		return 0, fmt.Errorf("vllm: count tokens: response must contain a nonnegative integer count")
	}
	return *result.Count, nil
}
