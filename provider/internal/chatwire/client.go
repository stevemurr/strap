// Package chatwire shares Chat Completions transport and message translation
// between concrete adapters. It owns no model presets or conversation state.
package chatwire

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

type Client struct {
	endpoint string
	http     *http.Client
}

func New(baseURL string, client *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("base URL must be an absolute HTTP(S) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("base URL must not contain credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	u.Path += "/chat/completions"
	u.RawPath = ""
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{endpoint: u.String(), http: client}, nil
}

// HTTPError retains a bounded diagnostic without interpreting backend policy.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// Submit accepts an adapter-owned wire struct, including its typed extensions.
// It never retries or rewrites options after an HTTP rejection.
func (c *Client) Submit(ctx context.Context, wire any) (provider.Response, error) {
	data, err := json.Marshal(wire)
	if err != nil {
		return provider.Response{}, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return provider.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return provider.Response{}, fmt.Errorf("submit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Preserve even a partial error body if reading the diagnostic fails.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return provider.Response{}, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	var result completion
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return provider.Response{}, fmt.Errorf("decode response: %w", err)
	}
	return decode(result)
}
