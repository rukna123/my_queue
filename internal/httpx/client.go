// Package httpx provides a small HTTP client wrapper with retries, timeouts,
// and exponential back-off. No locks are used; the Client struct is safe for
// concurrent use because its fields are immutable after construction and the
// underlying http.Client is concurrency-safe.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client wraps net/http.Client with retry and timeout behaviour.
type Client struct {
	http       *http.Client
	maxRetries int
	baseDelay  time.Duration
}

// NewClient creates a Client with the given timeout and retry count.
func NewClient(timeout time.Duration, maxRetries int) *Client {
	return &Client{
		http: &http.Client{
			Timeout: timeout,
		},
		maxRetries: maxRetries,
		baseDelay:  500 * time.Millisecond,
	}
}

// Do executes the request with retries on transient failures (5xx or network errors).
// It uses exponential back-off between attempts.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)

	for attempt := range c.maxRetries + 1 {
		resp, err = c.http.Do(req.Clone(ctx))
		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}

		// Drain body on retry to allow connection reuse.
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if attempt < c.maxRetries {
			delay := c.baseDelay * (1 << uint(attempt))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("httpx: all %d retries failed: %w", c.maxRetries+1, err)
	}
	return resp, nil
}

// Get is a convenience method for GET requests.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("httpx: new request: %w", err)
	}
	return c.Do(ctx, req)
}
