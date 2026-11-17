// Package httpx is the small JSON-over-HTTP client the providers share.
package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
)

// Error is a non-2xx or non-JSON response.
type Error struct {
	StatusCode int
	Message    string // the API's own message when it sent one
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// Client sends GET requests carrying Header and decodes JSON responses.
type Client struct {
	HTTP   *http.Client // nil means a client with a 30s timeout
	Header http.Header
}

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// GetJSON fetches url into out and returns the response headers (for pagination).
func (c *Client) GetJSON(ctx context.Context, url string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.Header {
		req.Header[k] = v
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "yoink")

	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return resp.Header, err
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	isJSON := mediaType == "application/json" || mediaType == "application/vnd.github+json"

	if resp.StatusCode < 200 || resp.StatusCode > 299 || !isJSON {
		e := &Error{StatusCode: resp.StatusCode}
		var msg struct {
			Message string `json:"message"`
		}
		if isJSON && json.Unmarshal(body, &msg) == nil {
			e.Message = msg.Message
		} else if !isJSON && resp.StatusCode < 300 {
			// Azure DevOps answers an unauthenticated request with 203 and a sign-in page.
			e.Message = "expected JSON, got " + mediaType + " (not signed in?)"
		}
		return resp.Header, e
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.Header, fmt.Errorf("decoding %s: %w", url, err)
	}
	return resp.Header, nil
}
