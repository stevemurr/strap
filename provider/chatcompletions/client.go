// Package chatcompletions implements provider.Provider over the Chat Completions
// HTTP protocol used by local servers such as LM Studio. It never executes tools.
package chatcompletions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

type Config struct {
	// BaseURL is the server root or API prefix, for example http://localhost:1234
	// or http://localhost:1234/v1. A server root defaults to the /v1 prefix.
	BaseURL string
	Model   string
	// HTTPClient is optional. Submit uses its context for cancellation/deadlines.
	HTTPClient *http.Client
}

// Client is immutable after construction and may be shared by concurrent agents.
type Client struct {
	model string
	wire  *chatwire.Client
}

var _ provider.Provider = (*Client)(nil)

func New(config Config) (*Client, error) {
	wire, err := chatwire.New(config.BaseURL, config.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("chatcompletions: %w", err)
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("chatcompletions: model is required")
	}
	return &Client{model: config.Model, wire: wire}, nil
}

// HTTPError preserves the status and a bounded server diagnostic. No request is
// retried automatically; retry policy does not belong in this first adapter.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("chatcompletions: HTTP %d: %s", e.StatusCode, e.Body)
}

func (c *Client) Submit(ctx context.Context, input provider.Request) (provider.Response, error) {
	wire, err := chatwire.Encode(c.model, input)
	if err != nil {
		return provider.Response{}, fmt.Errorf("chatcompletions: encode content: %w", err)
	}
	result, err := c.wire.Submit(ctx, wire)
	if err != nil {
		var responseError *chatwire.HTTPError
		if errors.As(err, &responseError) {
			return provider.Response{}, &HTTPError{StatusCode: responseError.StatusCode, Body: responseError.Body}
		}
		return result, fmt.Errorf("chatcompletions: %w", err)
	}
	return result, nil
}
