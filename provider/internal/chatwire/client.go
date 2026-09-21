// Package chatwire shares Chat Completions transport and message translation
// between concrete adapters. It owns no model presets or conversation state.
package chatwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/stevemurr/strap/provider"
)

type Client struct {
	adapter  string
	endpoint string
	http     *http.Client
}

// New builds a client for one adapter; adapter prefixes every error it returns.
func New(adapter, baseURL string, client *http.Client) (*Client, error) {
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
	return &Client{adapter: adapter, endpoint: u.String(), http: client}, nil
}

// HTTPError retains a bounded diagnostic without interpreting backend policy.
// Adapter names the client that received it. No retry with altered
// parameters is performed.
type HTTPError struct {
	Adapter    string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Adapter == "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Adapter, e.StatusCode, e.Body)
}

// fail prefixes transport and decoding errors with the adapter name; an
// HTTPError already carries it.
func (c *Client) fail(err error) error {
	var status *HTTPError
	if err == nil || c.adapter == "" || errors.As(err, &status) {
		return err
	}
	return fmt.Errorf("%s: %w", c.adapter, err)
}

// do posts wire as JSON and returns a successful response, whose body the
// caller owns. A rejected status becomes an HTTPError with a bounded body.
func (c *Client) do(ctx context.Context, endpoint string, wire any, accept string) (*http.Response, error) {
	data, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("submit: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Preserve even a partial error body if reading the diagnostic fails.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &HTTPError{Adapter: c.adapter, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

// Submit accepts an adapter-owned wire struct, including its typed extensions.
// It never retries or rewrites options after an HTTP rejection.
func (c *Client) Submit(ctx context.Context, wire any, observer provider.Observer) (provider.Response, error) {
	result, err := c.submit(ctx, wire, observer)
	return result, c.fail(err)
}

func (c *Client) submit(ctx context.Context, wire any, observer provider.Observer) (provider.Response, error) {
	resp, err := c.do(ctx, c.endpoint, wire, "text/event-stream")
	if err != nil {
		return provider.Response{}, err
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readStream(resp.Body, observer)
	}
	// Some compatible servers return a complete JSON response to streaming requests.
	var result completion
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return provider.Response{}, fmt.Errorf("decode response: %w", err)
	}
	if err := observeCompletion(result, observer); err != nil {
		return provider.Response{Usage: decodeUsage(result.Usage)}, err
	}
	return decode(result)
}

// Post shares JSON transport with adapter-owned endpoints such as tokenization.
// Adapters own endpoint selection, request fields, and response validation.
func (c *Client) Post(ctx context.Context, endpoint string, wire, result any) error {
	resp, err := c.do(ctx, endpoint, wire, "")
	if err != nil {
		return c.fail(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return c.fail(fmt.Errorf("decode response: %w", err))
	}
	return nil
}
