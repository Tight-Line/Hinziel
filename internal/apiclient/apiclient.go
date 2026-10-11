// Package apiclient is the small JSON-over-HTTP client the components in this
// repository share. Rate limits and server errors come back as
// core.RetryableError, so the engine retries the step with backoff.
package apiclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
)

// Client calls one API. Auth sets credentials on every request.
type Client struct {
	Name    string // used in error messages: "trello", "gitlab", …
	BaseURL string // no trailing slash
	Auth    func(*http.Request)
	HTTP    *http.Client // nil = a client with a 60s timeout
}

// StatusError is a response with a status code outside 2xx.
type StatusError struct {
	API    string
	Method string
	Path   string
	Status int
	Body   string // the start of the response body
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %s %s: %d %s", e.API, e.Method, e.Path, e.Status, e.Body)
}

// IsStatus reports whether err is a StatusError with one of the codes.
func IsStatus(err error, codes ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, c := range codes {
		if se.Status == c {
			return true
		}
	}
	return false
}

var defaultHTTP = &http.Client{Timeout: 60 * time.Second}

// Do sends a request. A non-nil body is sent as JSON; a non-nil out receives
// the JSON response. path may carry its own query; query is added to it.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.Raw(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: %s %s: decoding response: %w", c.Name, method, path, err)
	}
	return nil
}

// RawBody is a request body sent as it is rather than as JSON, such as a
// multipart upload.
type RawBody struct {
	ContentType string
	Data        []byte
}

// Raw sends a request and returns a 2xx response for the caller to read and
// close. Other statuses become a *StatusError.
func (c *Client) Raw(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + query.Encode()
	}
	var rd io.Reader
	contentType := "application/json"
	if raw, ok := body.(RawBody); ok {
		rd, contentType = bytes.NewReader(raw.Data), raw.ContentType
	} else if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Auth != nil {
		c.Auth(req)
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		var unknownCA x509.UnknownAuthorityError
		if errors.As(err, &unknownCA) {
			return nil, fmt.Errorf("%s: %s %s: %w (are CA certificates installed? e.g. the ca-certificates package)", c.Name, method, path, err)
		}
		// The request may not have reached the server at all.
		return nil, &core.RetryableError{Err: fmt.Errorf("%s: %s %s: %w", c.Name, method, path, err)}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	se := &StatusError{API: c.Name, Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &core.RetryableError{Err: se}
	}
	return nil, se
}
