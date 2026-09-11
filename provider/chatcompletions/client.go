// Package chatcompletions implements provider.Provider over the Chat Completions
// HTTP protocol used by local servers such as LM Studio. It never executes tools.
package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/stevemurr/strap/provider"
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
	endpoint string
	model    string
	http     *http.Client
}

var _ provider.Provider = (*Client)(nil)

func New(config Config) (*Client, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("chatcompletions: base URL must be an absolute HTTP(S) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("chatcompletions: base URL must not contain credentials, query, or fragment")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("chatcompletions: model is required")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	u.Path += "/chat/completions"
	u.RawPath = ""
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{endpoint: u.String(), model: config.Model, http: client}, nil
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

func (c *Client) Submit(ctx context.Context, request provider.Request) (provider.Response, error) {
	wire, err := encode(c.model, request)
	if err != nil {
		return provider.Response{}, fmt.Errorf("chatcompletions: encode content: %w", err)
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return provider.Response{}, fmt.Errorf("chatcompletions: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return provider.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return provider.Response{}, fmt.Errorf("chatcompletions: submit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return provider.Response{}, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	var result completion
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return provider.Response{}, fmt.Errorf("chatcompletions: decode response: %w", err)
	}
	return decode(result)
}
