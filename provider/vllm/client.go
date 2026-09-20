// Package vllm implements provider.Provider using vLLM's Chat Completions API.
// It owns vLLM option validation and serialization, not model presets or history.
package vllm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

type Config struct {
	// BaseURL accepts a server root or API prefix. A root defaults to /v1
	// for generation. Counting removes a trailing /v1 and appends /tokenize,
	// preserving any preceding reverse-proxy path.
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	Generation Generation
}

// Client snapshots generation settings at construction and is safe to share
// across agents. The injected HTTPClient remains a shared collaborator.
type Client struct {
	model            string
	wire             *chatwire.Client
	generation       generationFields
	tokenizeEndpoint string
}

var _ provider.Provider = (*Client)(nil)
var _ provider.TokenCounter = (*Client)(nil)
var _ provider.OutputTokenLimiter = (*Client)(nil)

// OutputTokenLimit returns an independent copy of the configured per-call cap.
func (c *Client) OutputTokenLimit() *int64 {
	if c.generation.MaxTokens == nil {
		return nil
	}
	limit := int64(*c.generation.MaxTokens)
	return &limit
}

func New(config Config) (*Client, error) {
	wire, err := chatwire.New(config.BaseURL, config.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("vllm: %w", err)
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("vllm: model is required")
	}
	generation, err := config.Generation.freeze()
	if err != nil {
		return nil, fmt.Errorf("vllm: %w", err)
	}
	// chatwire.New already validated the URL. Tokenization lives outside /v1.
	u, _ := url.Parse(config.BaseURL)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1") + "/tokenize"
	u.RawPath = ""
	return &Client{model: config.Model, wire: wire, generation: generation, tokenizeEndpoint: u.String()}, nil
}

// HTTPError preserves rejected options and other bounded server diagnostics.
// No retry with altered parameters is performed.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("vllm: HTTP %d: %s", e.StatusCode, e.Body)
}

func (c *Client) Submit(ctx context.Context, input provider.Request, observer provider.Observer) (provider.Response, error) {
	base, err := chatwire.Encode(c.model, input)
	if err != nil {
		return provider.Response{}, fmt.Errorf("vllm: encode content: %w", err)
	}
	// Embed typed fields at the root. extra_body is an SDK convention, not a
	// vLLM wire field. Only chat template options have a nested JSON object.
	toolChoice := ""
	if len(base.Tools) > 0 {
		toolChoice = "auto"
	}
	wire := struct {
		chatwire.Request
		generationFields
		ToolChoice string `json:"tool_choice,omitempty"`
	}{base, c.generation, toolChoice}
	result, err := c.wire.Submit(ctx, wire, observer)
	if err != nil {
		var responseError *chatwire.HTTPError
		if errors.As(err, &responseError) {
			return provider.Response{}, &HTTPError{StatusCode: responseError.StatusCode, Body: responseError.Body}
		}
		return result, fmt.Errorf("vllm: %w", err)
	}
	return result, nil
}
